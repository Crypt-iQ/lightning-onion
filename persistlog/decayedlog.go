package persistlog

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"github.com/boltdb/bolt"
	"github.com/lightningnetwork/lnd/channeldb"
	"github.com/lightningnetwork/lnd/lnwire"
	"sync"
	"time"
)

const (
	// defaultDbDirectory is the default directory where our decayed log
	// will store our (sharedHash, CLTV expiry height) key-value pairs.
	defaultDbDirectory = "sharedsecret"

	// sharedHashSize is the size in bytes of the keys we will be storing
	// in the DecayedLog. It represents the first 20 bytes of a truncated
	// sha-256 hash of a secret generated by ECDH.
	sharedHashSize = 20

	// sharedSecretSize is the size in bytes of the shared secrets.
	sharedSecretSize = 32
)

var (
	// openChannelBucket is a bucket which stores all of the currently
	// open channels. It has a second, nested bucket which is keyed by
	// the Sha-256 hash of the shared secret used in the sphinx routing
	// protocol for a particular received HTLC.
	openChannelBucket = []byte("open-channel")

	// sharedHashBucket is a bucket which houses all the first sharedHashSize
	// bytes of a received HTLC's hashed shared secret and the HTLC's
	// expiry block height.
	sharedHashBucket = []byte("shared-hash")
)

// DecayedLog implements the PersistLog interface. It stores the first
// sharedHashSize bytes of a sha256-hashed shared secret along with a node's
// CLTV value. It is a decaying log meaning there will be a garbage collector
// to collect entries which are expired according to their stored CLTV value
// and the current block height. DecayedLog wraps channeldb for simplicity, but
// must batch writes to the database to decrease write contention.
type DecayedLog struct {
	db   *channeldb.DB
	wg   sync.WaitGroup
	quit chan (struct{})
}

// garbageCollector deletes entries from sharedHashBucket whose expiry height
// has already past. This function MUST be run as a goroutine.
func (d *DecayedLog) garbageCollector() error {
	defer d.wg.Done()

outer:
	for {
		select {
		case <-time.After(60 * time.Second):
			// TODO(eugene) logic here
			fmt.Println("hello")
		case <-d.quit:
			break outer
		}
	}

	return nil
}

// A compile time check to see if DecayedLog adheres to the PersistLog
// interface.
var _ PersistLog = (*DecayedLog)(nil)

// hashSharedSecret Sha-256 hashes the shared secret and returns the first
// sharedHashSize bytes of the hash.
func hashSharedSecret(sharedSecret [sharedSecretSize]byte) [sharedHashSize]byte {
	h := sha256.New()
	h.Write(sharedSecret[:])
	return h.Sum(nil)[:sharedHashSize]
}

// Delete removes a <shared secret hash, CLTV value> key-pair from the
// sharedHashBucket.
func (d *DecayedLog) Delete(shortChanID *lnwire.ShortChannelID, hash []byte) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		var (
			b       bytes.Buffer
			scratch [8]byte
		)

		openChannels, err := tx.CreateBucketIfNotExists(openChannelBucket)
		if err != nil {
			return err
		}

		sharedHashes, err := openChannels.CreateBucketIfNotExists(sharedHashBucket)
		if err != nil {
			return err
		}

		if err := sharedHashes.Delete(hash); err != nil {
			return err
		}

		binary.BigEndian.PutUint64(scratch[:8], shortChanID.ToUint64())
		if _, err := b.Write(scratch[:8]); err != nil {
			return err
		}

		return openChannels.Delete(b.Bytes())
	})
}

// Get retrieves the CLTV value of a processed HTLC given the first 20 bytes
// of the Sha-256 hash of the shared secret used during sphinx processing.
func (d *DecayedLog) Get(shortChanID *lnwire.ShortChannelID, hash []byte) (
	uint32, error) {
	var (
		b       bytes.Buffer
		scratch [8]byte
		value   uint32
	)

	err := d.db.View(func(tx *bolt.Tx) error {
		// First grab the open channels bucket which stores the mapping
		// from serialized ShortChannelID to shared secret hash.
		openChannels := tx.Bucket(openChannelBucket)
		if openChannels == nil {
			return fmt.Errorf("openChannelBucket is nil, could " +
				"not retrieve CLTV value")
		}

		// Serialize ShortChannelID
		binary.BigEndian.PutUint64(scratch[:8], shortChanID.ToUint64())
		if _, err := b.Write(scratch[:8]); err != nil {
			return err
		}

		// If a key for this ShortChannelID isn't found, then the
		// target node doesn't exist within the database.
		dbHash := openChannels.Get(b.Bytes())
		if dbHash == nil {
			return fmt.Errorf("openChannelBucket does not contain "+
				"ShortChannelID(%v)", shortChanID)
		}

		// Grab the shared hash bucket which stores the mapping from
		// truncated sha-256 hashes of shared secrets to CLTV values.
		sharedHashes := openChannels.Bucket(sharedHashBucket)
		if sharedHashes == nil {
			return fmt.Errorf("sharedHashes is nil, could " +
				"not retrieve CLTV value")
		}

		// If the sharedHash is found, we use it to find the associated
		// CLTV in the sharedHashBucket.
		value = sharedHashes.Get(dbHash)
		if value == nil {
			return fmt.Errorf("sharedHashBucket does not contain "+
				"sharedHash(%s)", string(dbHash))
		}

		return nil
	})
	if err != nil {
		return value, err
	}

	return value, nil
}

// Put stores a <shared secret hash, CLTV value> key-pair into the
// sharedHashBucket.
func (d *DecayedLog) Put(shortChanID *lnwire.ShortChannelID, hash []byte,
	value uint32) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		var (
			b       bytes.Buffer
			scratch [8]byte
		)

		openChannels, err := tx.CreateBucketIfNotExists(openChannelBucket)
		if err != nil {
			return err
		}

		sharedHashes, err := openChannels.CreateBucketIfNotExists(sharedHashBucket)
		if err != nil {
			return err
		}

		binary.BigEndian.PutUint32(scratch[:4], value)
		if _, err := b.Write(scratch[:4]); err != nil {
			return err
		}

		if err := sharedHashes.Put(hash, b.Bytes()); err != nil {
			return err
		}

		// Reset the Buffer so we can store shortChanID in it.
		b.Reset()

		binary.BigEndian.PutUint64(scratch[:8], shortChanID.ToUint64())
		if _, err := b.Write(scratch[:8]); err != nil {
			return err
		}

		return openChannels.Put(b.Bytes(), hash)
	})
}

// Start opens the database we will be using to store hashed shared secrets.
// It also starts the garbage collector in a goroutine to remove stale
// database entries.
func (d *DecayedLog) Start() error {
	// Open the channeldb for use.
	var err error
	if d.db, err = channeldb.Open(defaultDbDirectory); err != nil {
		return fmt.Errorf("Could not open channeldb: %v", err)
	}

	// Start garbage collector.
	d.wg.Add(1)
	go d.garbageCollector()

	return nil
}

// Stop halts the garbage collector and closes channeldb.
func (d *DecayedLog) Stop() {
	// Stop garbage collector.
	close(d.quit)

	// Close channeldb.
	d.db.Close()
}
