package persistlog

import (
	"crypto/sha256"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/roasbeef/btcd/btcec"
	"testing"
)

func generateSharedSecret(pub *btcec.PublicKey, priv *btcec.PrivateKey) [32]byte {
	s := &btcec.PublicKey{}
	x, y := btcec.S256().ScalarMult(pub.X, pub.Y, priv.D.Bytes())
	s.X = x
	s.Y = y

	return sha256.Sum256(s.SerializeCompressed())
}

func testDecayedLogStorage(t *testing.T) {
	t.Parallel()

	d := DecayedLog{}

	err := d.Start()
	if err != nil {
		t.Fatalf("Unable to start / open DecayedLog")
	}
	defer d.Stop()

	priv1, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		t.Fatalf("Unable to create new private key 1")
	}

	priv2, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		t.Fatalf("Unable to create new private key 2")
	}

	secret := generateSharedSecret(priv1.PublicKey, priv2)
	shortChanID := lnwire.NewShortChanIDFromInt(uint64(102031))
	hashedSecret := hashSharedSecret(secret)

	err = d.Put(shortChanID, hashedSecret, 100)
	if err != nil {
		t.Fatalf("Unable to store in channeldb")
	}

}
