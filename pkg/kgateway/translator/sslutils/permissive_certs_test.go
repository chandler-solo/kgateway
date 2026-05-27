package sslutils

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/util/cert"
)

// mustNegativeSerialCert mints a self-signed certificate whose serial number
// is negative. Go 1.23+ rejects non-positive serials in x509.CreateCertificate,
// so we generate a normal positive-serial cert and then surgically rewrite the
// serial-number bytes in the DER to a negative value. The signature won't
// verify but the permissive parser is signature-agnostic.
func mustNegativeSerialCert(t *testing.T) (keyPEM, certPEM, certDER []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), // gets rewritten to -1 below
		Subject:      pkix.Name{CommonName: "negative-serial.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	posDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	certDER = injectNegativeSerial(t, posDER)
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return keyPEM, certPEM, certDER
}

// injectNegativeSerial walks the certificate DER to the serial-number INTEGER
// and rewrites its first value byte to 0xFF, making the integer negative
// without changing its length.
func injectNegativeSerial(t *testing.T, der []byte) []byte {
	t.Helper()
	pos := skipTagAndLen(t, der, 0, 0x30)  // outer SEQUENCE -> contents of Certificate
	pos = skipTagAndLen(t, der, pos, 0x30) // TBSCertificate SEQUENCE -> contents
	if der[pos] == 0xA0 {                  // optional [0] EXPLICIT Version
		_, hdrLen, contentLen := readTagAndLen(t, der, pos)
		pos += hdrLen + contentLen
	}
	require.Equal(t, byte(0x02), der[pos], "expected INTEGER tag for serial number")
	_, hdrLen, contentLen := readTagAndLen(t, der, pos)
	require.Greater(t, contentLen, 0)
	out := make([]byte, len(der))
	copy(out, der)
	// Make the high bit of the first content byte 1 (negative in two's complement).
	out[pos+hdrLen] = 0xFF
	return out
}

func skipTagAndLen(t *testing.T, der []byte, pos int, expectedTag byte) int {
	t.Helper()
	gotTag, hdrLen, _ := readTagAndLen(t, der, pos)
	require.Equal(t, expectedTag, gotTag)
	return pos + hdrLen
}

func readTagAndLen(t *testing.T, der []byte, pos int) (tag byte, hdrLen int, contentLen int) {
	t.Helper()
	require.Less(t, pos, len(der))
	tag = der[pos]
	lenByte := der[pos+1]
	if lenByte&0x80 == 0 {
		return tag, 2, int(lenByte)
	}
	n := int(lenByte & 0x7f)
	require.GreaterOrEqual(t, n, 1)
	require.LessOrEqual(t, n, 4)
	length := 0
	for i := range n {
		length = length<<8 | int(der[pos+2+i])
	}
	return tag, 2 + n, length
}

func mustPositiveSerialCert(t *testing.T) (keyPEM, certPEM []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(123),
		Subject:      pkix.Name{CommonName: "positive-serial.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return keyPEM, certPEM
}

// Sanity check: ensure the upstream parser actually rejects negative-serial
// certs in this Go version, so the test fixture meaningfully exercises the
// permissive code path.
func TestNegativeSerialCertIsRejectedByStdlib(t *testing.T) {
	_, _, certDER := mustNegativeSerialCert(t)
	_, err := x509.ParseCertificate(certDER)
	require.Error(t, err, "stdlib should reject the negative-serial fixture; if this passes, the permissive path is no longer exercised")
	assert.Contains(t, err.Error(), "negative serial number")
}

func TestParseCertsPEMPermissive_NegativeSerial(t *testing.T) {
	_, certPEM, certDER := mustNegativeSerialCert(t)

	got, err := ParseCertsPEMPermissive(certPEM)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, certDER, got[0].Raw, "Raw should hold the original DER so cert.EncodeCertificates round-trips")

	// Round-trip through cert.EncodeCertificates (used by callers) must work.
	encoded, err := cert.EncodeCertificates(got...)
	require.NoError(t, err)
	block, _ := pem.Decode(encoded)
	require.NotNil(t, block)
	assert.Equal(t, certDER, block.Bytes)
}

func TestParseCertsPEMPermissive_NormalCert(t *testing.T) {
	_, certPEM := mustPositiveSerialCert(t)

	got, err := ParseCertsPEMPermissive(certPEM)
	require.NoError(t, err)
	require.Len(t, got, 1)
	// For a positive-serial cert, the strict parser populates the full struct.
	assert.Equal(t, "positive-serial.test", got[0].Subject.CommonName)
}

func TestParseCertsPEMPermissive_MixedChain(t *testing.T) {
	_, negPEM, _ := mustNegativeSerialCert(t)
	_, posPEM := mustPositiveSerialCert(t)
	chain := append([]byte{}, negPEM...)
	chain = append(chain, posPEM...)

	got, err := ParseCertsPEMPermissive(chain)
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestParseCertsPEMPermissive_Garbage(t *testing.T) {
	_, err := ParseCertsPEMPermissive([]byte("not a cert"))
	assert.Error(t, err)

	bogusBlock := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{0x30, 0x05, 0xff, 0xff, 0xff}})
	_, err = ParseCertsPEMPermissive(bogusBlock)
	assert.Error(t, err)
}

func TestValidateCertKeyPairPermissive_NegativeSerial(t *testing.T) {
	keyPEM, certPEM, _ := mustNegativeSerialCert(t)
	assert.NoError(t, ValidateCertKeyPairPermissive(certPEM, keyPEM))
}

func TestValidateCertKeyPairPermissive_NormalCert(t *testing.T) {
	keyPEM, certPEM := mustPositiveSerialCert(t)
	assert.NoError(t, ValidateCertKeyPairPermissive(certPEM, keyPEM))
}

func TestValidateCertKeyPairPermissive_Mismatch(t *testing.T) {
	_, certPEM, _ := mustNegativeSerialCert(t)
	wrongKeyPEM, _ := mustPositiveSerialCert(t)
	err := ValidateCertKeyPairPermissive(certPEM, wrongKeyPEM)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")
}
