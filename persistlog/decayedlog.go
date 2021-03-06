package persistlog

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"sync"

	"github.com/boltdb/bolt"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/channeldb"
)

const (
	// defaultDbDirectory is the default directory where our decayed log
	// will store our (sharedHash, CLTV) key-value pairs.
	defaultDbDirectory = "sharedhashes"

	// sharedHashSize is the size in bytes of the keys we will be storing
	// in the DecayedLog. It represents the first 20 bytes of a truncated
	// sha-256 hash of a secret generated by ECDH.
	sharedHashSize = 20

	// sharedSecretSize is the size in bytes of the shared secrets.
	sharedSecretSize = 32
)

var (
	// sharedHashBucket is a bucket which houses the first sharedHashSize
	// bytes of a received HTLC's hashed shared secret as the key and the HTLC's
	// CLTV expiry as the value.
	sharedHashBucket = []byte("shared-hash")
)

// DecayedLog implements the PersistLog interface. It stores the first
// sharedHashSize bytes of a sha256-hashed shared secret along with a node's
// CLTV value. It is a decaying log meaning there will be a garbage collector
// to collect entries which are expired according to their stored CLTV value
// and the current block height. DecayedLog wraps channeldb for simplicity and
// batches writes to the database to decrease write contention.
type DecayedLog struct {
	db       *channeldb.DB
	wg       sync.WaitGroup
	quit     chan (struct{})
	Notifier chainntnfs.ChainNotifier
}

// garbageCollector deletes entries from sharedHashBucket whose expiry height
// has already past. This function MUST be run as a goroutine.
func (d *DecayedLog) garbageCollector() error {
	defer d.wg.Done()

	epochClient, err := d.Notifier.RegisterBlockEpochNtfn()
	if err != nil {
		return fmt.Errorf("Unable to register for epoch "+
			"notification: %v", err)
	}
	defer epochClient.Cancel()

outer:
	for {
		select {
		case epoch, ok := <-epochClient.Epochs:
			if !ok {
				return fmt.Errorf("Epoch client shutting " +
					"down")
			}

			err := d.db.Batch(func(tx *bolt.Tx) error {
				// Grab the shared hash bucket
				sharedHashes := tx.Bucket(sharedHashBucket)
				if sharedHashes == nil {
					return fmt.Errorf("sharedHashBucket " +
						"is nil")
				}

				var expiredCltv [][]byte
				sharedHashes.ForEach(func(k, v []byte) error {
					// The CLTV value in question.
					cltv := uint32(binary.BigEndian.Uint32(v))

					// Current blockheight
					height := uint32(epoch.Height)

					if cltv < height {
						// This CLTV is expired. We must
						// add it to an array which we'll
						// loop over and delete every
						// hash contained from the db.
						expiredCltv = append(expiredCltv, k)
					}

					return nil
				})

				// Delete every item in the array. This must
				// be done explicitly outside of the ForEach
				// function for safety reasons.
				for _, hash := range expiredCltv {
					err := sharedHashes.Delete(hash)
					if err != nil {
						return err
					}
				}

				return nil
			})
			if err != nil {
				return fmt.Errorf("Error viewing channeldb: "+
					"%v", err)
			}

		case <-d.quit:
			break outer
		}
	}

	return nil
}

// A compile time check to see if DecayedLog adheres to the PersistLog
// interface.
var _ PersistLog = (*DecayedLog)(nil)

// HashSharedSecret Sha-256 hashes the shared secret and returns the first
// sharedHashSize bytes of the hash.
func HashSharedSecret(sharedSecret [sharedSecretSize]byte) [sharedHashSize]byte {
	// Sha256 hash of sharedSecret
	h := sha256.New()
	h.Write(sharedSecret[:])

	var sharedHash [sharedHashSize]byte

	// Copy bytes to sharedHash
	copy(sharedHash[:], h.Sum(nil)[:sharedHashSize])
	return sharedHash
}

// Delete removes a <shared secret hash, CLTV> key-pair from the
// sharedHashBucket.
func (d *DecayedLog) Delete(hash []byte) error {
	return d.db.Batch(func(tx *bolt.Tx) error {
		sharedHashes, err := tx.CreateBucketIfNotExists(sharedHashBucket)
		if err != nil {
			return fmt.Errorf("Unable to created sharedHashes bucket:"+
				" %v", err)
		}

		return sharedHashes.Delete(hash)
	})
}

// Get retrieves the CLTV of a processed HTLC given the first 20 bytes of the
// Sha-256 hash of the shared secret.
func (d *DecayedLog) Get(hash []byte) (uint32, error) {
	// math.MaxUint32 is returned when Get did not retrieve a value.
	// This was chosen because it's not feasible for a CLTV to be this high.
	var value uint32 = math.MaxUint32

	err := d.db.View(func(tx *bolt.Tx) error {
		// Grab the shared hash bucket which stores the mapping from
		// truncated sha-256 hashes of shared secrets to CLTV's.
		sharedHashes := tx.Bucket(sharedHashBucket)
		if sharedHashes == nil {
			return fmt.Errorf("sharedHashes is nil, could " +
				"not retrieve CLTV value")
		}

		// Retrieve the bytes which represents the CLTV
		valueBytes := sharedHashes.Get(hash)
		if valueBytes == nil {
			return nil
		}

		// The first 4 bytes represent the CLTV, store it in value.
		value = uint32(binary.BigEndian.Uint32(valueBytes))

		return nil
	})
	if err != nil {
		return value, err
	}

	return value, nil
}

// Put stores a shared secret hash as the key and the CLTV as the value.
func (d *DecayedLog) Put(hash []byte, cltv uint32) error {
	// The CLTV will be stored into scratch and then stored into the
	// sharedHashBucket.
	var scratch [4]byte

	// Store value into scratch
	binary.BigEndian.PutUint32(scratch[:], cltv)

	return d.db.Batch(func(tx *bolt.Tx) error {
		sharedHashes, err := tx.CreateBucketIfNotExists(sharedHashBucket)
		if err != nil {
			return fmt.Errorf("Unable to create bucket sharedHashes:"+
				" %v", err)
		}

		return sharedHashes.Put(hash, scratch[:])
	})
}

// Start opens the database we will be using to store hashed shared secrets.
// It also starts the garbage collector in a goroutine to remove stale
// database entries.
func (d *DecayedLog) Start(dbDir string) error {
	// Create the quit channel
	d.quit = make(chan struct{})

	var directory string
	if dbDir == "" {
		directory = defaultDbDirectory
	} else {
		directory = dbDir
	}

	// Open the channeldb for use.
	var err error
	if d.db, err = channeldb.Open(directory); err != nil {
		return fmt.Errorf("Could not open channeldb: %v", err)
	}

	err = d.db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(sharedHashBucket)
		if err != nil {
			return fmt.Errorf("Unable to create bucket sharedHashes:"+
				" %v", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Start garbage collector.
	if d.Notifier != nil {
		d.wg.Add(1)
		go d.garbageCollector()
	}

	return nil
}

// Stop halts the garbage collector and closes channeldb.
func (d *DecayedLog) Stop() {
	// Stop garbage collector.
	close(d.quit)

	// Close channeldb.
	d.db.Close()
}
