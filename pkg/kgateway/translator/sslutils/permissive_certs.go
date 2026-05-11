package sslutils

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"

	"golang.org/x/crypto/cryptobyte"
	cryptobyte_asn1 "golang.org/x/crypto/cryptobyte/asn1"
)

// Go 1.23 changed crypto/x509.ParseCertificate to reject certificates with
// negative serial numbers by default. The transitional escape hatch
// GODEBUG=x509negativeserial=1 is documented for removal in a future Go
// release. Envoy accepts these certs, so kgateway must continue to accept
// them without depending on the GODEBUG flag.
//
// The helpers here delegate all real cert parsing to crypto/x509: if the
// strict parser rejects a cert solely because its serial is negative, we
// rewrite the first byte of the serial-number INTEGER to 0x01 (a
// minimally-encoded positive value), let the stdlib parse the rewritten
// bytes, and — for ParseCertsPEMPermissive — restore the original DER in
// Certificate.Raw so re-encoders see the user's original bytes.

// ParseCertsPEMPermissive is a drop-in replacement for
// k8s.io/client-go/util/cert.ParseCertsPEM that additionally accepts
// certificates with negative serial numbers.
func ParseCertsPEMPermissive(pemBytes []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	found := false
	rest := pemBytes
	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			continue
		}
		c, err := parseCertificateAllowingNegativeSerial(block.Bytes)
		if err != nil {
			return certs, err
		}
		certs = append(certs, c)
		found = true
	}
	if !found {
		return certs, errors.New("data does not contain any valid RSA or ECDSA certificates")
	}
	return certs, nil
}

// ValidateCertKeyPairPermissive verifies that the PEM-encoded certificate
// chain and private key are a valid pair, accepting leaf certs with negative
// serial numbers. It is used purely for validation; the parsed tls.Certificate
// is discarded.
func ValidateCertKeyPairPermissive(certPEM, keyPEM []byte) error {
	sanitized, err := rewriteNegativeSerialsInPEM(certPEM)
	if err != nil {
		return err
	}
	_, err = tls.X509KeyPair(sanitized, keyPEM)
	return err
}

// parseCertificateAllowingNegativeSerial calls x509.ParseCertificate on der.
// If parsing fails solely because of a negative serial, the serial is
// temporarily rewritten to a positive value, the cert is reparsed, and the
// returned Certificate.Raw is restored to the original DER.
func parseCertificateAllowingNegativeSerial(der []byte) (*x509.Certificate, error) {
	c, err := x509.ParseCertificate(der)
	if err == nil {
		return c, nil
	}
	if !isNegativeSerialError(err) {
		return nil, err
	}
	sanitized, err := rewriteSerialToPositive(der)
	if err != nil {
		return nil, err
	}
	c, err = x509.ParseCertificate(sanitized)
	if err != nil {
		return nil, err
	}
	c.Raw = der
	return c, nil
}

// rewriteNegativeSerialsInPEM returns pemBytes with any CERTIFICATE block
// whose serial number is negative rewritten to have a positive serial.
// Non-CERTIFICATE blocks pass through unchanged.
func rewriteNegativeSerialsInPEM(pemBytes []byte) ([]byte, error) {
	var out bytes.Buffer
	rewrote := false
	rest := pemBytes
	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" && len(block.Headers) == 0 && serialIsNegative(block.Bytes) {
			rewritten, err := rewriteSerialToPositive(block.Bytes)
			if err != nil {
				return nil, err
			}
			block.Bytes = rewritten
			rewrote = true
		}
		if err := pem.Encode(&out, block); err != nil {
			return nil, err
		}
	}
	if !rewrote {
		return pemBytes, nil
	}
	return out.Bytes(), nil
}

// rewriteSerialToPositive returns a copy of der with the first content byte
// of the serial-number INTEGER replaced by 0x01. The resulting INTEGER is a
// minimally-encoded positive value regardless of the original bytes, so the
// strict x509 parser accepts it. The cert's signature no longer matches but
// no signature verification happens on this path.
func rewriteSerialToPositive(der []byte) ([]byte, error) {
	off, err := serialContentOffset(der)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(der))
	copy(out, der)
	out[off] = 0x01
	return out, nil
}

func serialIsNegative(der []byte) bool {
	off, err := serialContentOffset(der)
	if err != nil {
		return false
	}
	return der[off]&0x80 != 0
}

// serialContentOffset returns the offset within der of the first content byte
// of the serial-number INTEGER in an X.509 Certificate DER.
func serialContentOffset(der []byte) (int, error) {
	input := cryptobyte.String(der)
	var outer cryptobyte.String
	if !input.ReadASN1(&outer, cryptobyte_asn1.SEQUENCE) {
		return 0, errors.New("x509: malformed certificate")
	}
	var tbs cryptobyte.String
	if !outer.ReadASN1(&tbs, cryptobyte_asn1.SEQUENCE) {
		return 0, errors.New("x509: malformed tbs certificate")
	}
	tbs.SkipOptionalASN1(cryptobyte_asn1.Tag(0).Constructed().ContextSpecific())
	if len(tbs) < 2 || tbs[0] != byte(cryptobyte_asn1.INTEGER) {
		return 0, errors.New("x509: malformed serial number")
	}
	// cryptobyte's slice reads preserve the backing array's full capacity, so
	// cap(der) - cap(tbs) is the absolute offset of tbs's first byte in der.
	// (Using len(der) is wrong when der itself has cap > len, as happens with
	// slices returned by pem.Decode.)
	serialTagOff := cap(der) - cap(tbs)
	lenByte := tbs[1]
	contentOff := serialTagOff + 2
	if lenByte >= 0x80 {
		n := int(lenByte & 0x7f)
		if n == 0 || 2+n > len(tbs) {
			return 0, errors.New("x509: malformed serial length")
		}
		contentOff += n
	}
	if contentOff >= len(der) {
		return 0, errors.New("x509: malformed serial content")
	}
	return contentOff, nil
}

func isNegativeSerialError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "negative serial number")
}
