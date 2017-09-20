package persistlog

import (
	"crypto/sha256"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/roasbeef/btcd/btcec"
	"testing"
)

var (
	// Bytes of a private key
	key = [32]byte{
		0x81, 0xb6, 0x37, 0xd8, 0xfc, 0xd2, 0xc6, 0xda,
		0x68, 0x59, 0xe6, 0x96, 0x31, 0x13, 0xa1, 0x17,
		0xd, 0xe7, 0x93, 0xe4, 0xb7, 0x25, 0xb8, 0x4d,
		0x1e, 0xb, 0x4c, 0xf9, 0x9e, 0xc5, 0x8c, 0xe9,
	}
)

// generateSharedSecret generates a shared secret given a public key and a
// private key. It is directly copied from sphinx.go.
func generateSharedSecret(pub *btcec.PublicKey, priv *btcec.PrivateKey) [32]byte {
	s := &btcec.PublicKey{}
	x, y := btcec.S256().ScalarMult(pub.X, pub.Y, priv.D.Bytes())
	s.X = x
	s.Y = y

	return sha256.Sum256(s.SerializeCompressed())
}

// TestDecayedLogInsertionAndRetrieval inserts a CLTV value into the nested
// sharedHashBucket and then deletes it and finally asserts that we can no
// longer retrieve it.
func TestDecayedLogInsertionAndDeletion(t *testing.T) {
	//
}

// TestDecayedLogStartAndStop ...
func TestDecayedLogStartAndStop(t *testing.T) {
	//
}

// TestDecayedLogStorageAndRetrieval stores a CLTV value and then retrieves it
// via the nested sharedHashBucket and finally asserts that the original stored
// and retrieved CLTV values are equal.
func TestDecayedLogStorageAndRetrieval(t *testing.T) {
	t.Parallel()

	cltv := uint32(100)
	shortChan := uint64(102031)

	// Create a DecayedLog object
	d := DecayedLog{}

	// Open the channeldb
	err := d.Start()
	if err != nil {
		t.Fatalf("Unable to start / open DecayedLog")
	}
	defer d.Stop()

	// Create a new private key on elliptice curve secp256k1
	priv, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		t.Fatalf("Unable to create new private key 2")
	}

	// Generate a public key from the key bytes above
	_, testPub := btcec.PrivKeyFromBytes(btcec.S256(), key[:])

	// Generate a shared secret with the public and private keys we made
	secret := generateSharedSecret(testPub, priv)

	// Create a new ShortChannelID given shortChan. This is used as a key
	// to retrieve the hashedSecret.
	shortChanID := lnwire.NewShortChanIDFromInt(shortChan)

	// Create the hashedSecret given the shared secret we just generated.
	// This is the first 20 bytes of the Sha-256 hash of the shared secret.
	// This is used as a key to retrieve the cltv value.
	hashedSecret := hashSharedSecret(secret)

	// Store <shortChanID, hashedSecret> in the openChannelBucket &
	// store <hashedSecret, cltv> in the sharedHashBucket.
	err = d.Put(&shortChanID, hashedSecret[:], cltv)
	if err != nil {
		t.Fatalf("Unable to store in channeldb")
	}

	// Retrieve the stored cltv value given the hashedSecret key.
	value, err := d.Get(&shortChanID, hashedSecret[:])
	if err != nil {
		t.Fatalf("Unable to retrieve from channeldb")
	}

	// If the original cltv value does not match the value retrieved,
	// then the test failed.
	if cltv != value {
		t.Fatalf("Value retrieved doesn't match value stored")
	}

}
