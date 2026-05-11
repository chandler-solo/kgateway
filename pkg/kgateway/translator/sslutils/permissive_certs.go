package sslutils

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/cryptobyte"
	cryptobyte_asn1 "golang.org/x/crypto/cryptobyte/asn1"
)

// Permissive parsing helpers that accept X.509 certificates whose serial
// numbers are negative. Go 1.23 made x509.ParseCertificate reject such certs
// by default, gated behind GODEBUG=x509negativeserial=1, and the Go team plans
// to remove that escape hatch in a future release. Envoy accepts these certs,
// so kgateway must continue to accept them too.
//
// The helpers below try the strict stdlib parser first. If the only objection
// was the serial number, they validate the certificate's ASN.1 envelope and
// extract the minimum information downstream callers need. Real cryptographic
// validation still happens in Envoy on the data plane.

const negativeSerialErr = "negative serial number"

// ParseCertsPEMPermissive parses one or more PEM-encoded certificates from the
// given bytes. It is a drop-in replacement for k8s.io/client-go/util/cert.ParseCertsPEM
// that additionally accepts certificates with negative serial numbers.
//
// When a certificate trips the strict parser only because of a negative serial,
// the returned *x509.Certificate has only Raw populated. That is sufficient for
// callers that re-encode through cert.EncodeCertificates (which reads Raw).
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
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			if !isNegativeSerialError(err) {
				return certs, err
			}
			c, err = permissiveParseCertificate(block.Bytes)
			if err != nil {
				return certs, err
			}
		}
		certs = append(certs, c)
		found = true
	}
	if !found {
		return certs, errors.New("data does not contain any valid RSA or ECDSA certificates")
	}
	return certs, nil
}

// permissiveParseCertificate validates that der is a well-formed X.509
// Certificate ASN.1 structure without enforcing the serial-number sign rule.
// On success the returned *x509.Certificate has only Raw populated.
func permissiveParseCertificate(der []byte) (*x509.Certificate, error) {
	input := cryptobyte.String(der)
	var body cryptobyte.String
	if !input.ReadASN1(&body, cryptobyte_asn1.SEQUENCE) {
		return nil, errors.New("x509: malformed certificate")
	}
	if !input.Empty() {
		return nil, errors.New("x509: trailing data after certificate")
	}
	var tbs cryptobyte.String
	if !body.ReadASN1Element(&tbs, cryptobyte_asn1.SEQUENCE) {
		return nil, errors.New("x509: malformed tbs certificate")
	}
	var sigAlg cryptobyte.String
	if !body.ReadASN1(&sigAlg, cryptobyte_asn1.SEQUENCE) {
		return nil, errors.New("x509: malformed signature algorithm")
	}
	var sig cryptobyte.String
	if !body.ReadASN1(&sig, cryptobyte_asn1.BIT_STRING) {
		return nil, errors.New("x509: malformed signature value")
	}
	return &x509.Certificate{Raw: der}, nil
}

// ValidateCertKeyPairPermissive verifies that the supplied PEM-encoded
// certificate chain and private key are a valid pair. It is a drop-in
// replacement for tls.X509KeyPair used purely for validation; the resulting
// tls.Certificate is intentionally not returned because the existing callers
// discard it.
//
// Like ParseCertsPEMPermissive, it accepts leaf certificates with negative
// serial numbers. The cert <-> key match is still enforced in the fallback
// path by extracting the SubjectPublicKeyInfo from the leaf cert manually and
// comparing it to the parsed private key.
func ValidateCertKeyPairPermissive(certPEM, keyPEM []byte) error {
	_, err := tls.X509KeyPair(certPEM, keyPEM)
	if err == nil {
		return nil
	}
	if !isNegativeSerialError(err) {
		return err
	}
	return validateCertKeyPairFallback(certPEM, keyPEM)
}

func validateCertKeyPairFallback(certPEM, keyPEM []byte) error {
	leafDER, err := firstCertificateDER(certPEM)
	if err != nil {
		return err
	}
	pubKey, err := extractPublicKey(leafDER)
	if err != nil {
		return fmt.Errorf("tls: failed to extract leaf public key: %w", err)
	}
	privKey, err := parsePrivateKeyPEM(keyPEM)
	if err != nil {
		return err
	}
	return matchPublicAndPrivateKeys(pubKey, privKey)
}

// firstCertificateDER returns the DER bytes of the first CERTIFICATE PEM block.
func firstCertificateDER(certPEM []byte) ([]byte, error) {
	rest := certPEM
	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" && len(block.Headers) == 0 {
			return block.Bytes, nil
		}
	}
	return nil, errors.New("tls: failed to find any PEM data in certificate input")
}

// extractPublicKey pulls the SubjectPublicKeyInfo from a DER-encoded X.509
// certificate and parses it as a PKIX public key. It does not enforce the
// negative-serial rule.
func extractPublicKey(der []byte) (any, error) {
	input := cryptobyte.String(der)
	var body cryptobyte.String
	if !input.ReadASN1(&body, cryptobyte_asn1.SEQUENCE) {
		return nil, errors.New("x509: malformed certificate")
	}
	var tbs cryptobyte.String
	if !body.ReadASN1(&tbs, cryptobyte_asn1.SEQUENCE) {
		return nil, errors.New("x509: malformed tbs certificate")
	}
	// TBSCertificate ::= SEQUENCE {
	//   version [0] EXPLICIT Version DEFAULT v1,
	//   serialNumber CertificateSerialNumber,
	//   signature AlgorithmIdentifier,
	//   issuer Name,
	//   validity Validity,
	//   subject Name,
	//   subjectPublicKeyInfo SubjectPublicKeyInfo,
	//   ...
	// }
	if !tbs.SkipOptionalASN1(cryptobyte_asn1.Tag(0).Constructed().ContextSpecific()) {
		return nil, errors.New("x509: malformed version")
	}
	if !tbs.SkipASN1(cryptobyte_asn1.INTEGER) {
		return nil, errors.New("x509: malformed serial number")
	}
	if !tbs.SkipASN1(cryptobyte_asn1.SEQUENCE) {
		return nil, errors.New("x509: malformed signature algorithm")
	}
	if !tbs.SkipASN1(cryptobyte_asn1.SEQUENCE) {
		return nil, errors.New("x509: malformed issuer")
	}
	if !tbs.SkipASN1(cryptobyte_asn1.SEQUENCE) {
		return nil, errors.New("x509: malformed validity")
	}
	if !tbs.SkipASN1(cryptobyte_asn1.SEQUENCE) {
		return nil, errors.New("x509: malformed subject")
	}
	var spki cryptobyte.String
	if !tbs.ReadASN1Element(&spki, cryptobyte_asn1.SEQUENCE) {
		return nil, errors.New("x509: malformed subjectPublicKeyInfo")
	}
	return x509.ParsePKIXPublicKey(spki)
}

// parsePrivateKeyPEM mirrors crypto/tls.parsePrivateKey, which is unexported.
func parsePrivateKeyPEM(keyPEM []byte) (any, error) {
	var keyDER []byte
	rest := keyPEM
	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "PRIVATE KEY" || strings.HasSuffix(block.Type, " PRIVATE KEY") {
			keyDER = block.Bytes
			break
		}
	}
	if keyDER == nil {
		return nil, errors.New("tls: failed to find any PEM data in key input")
	}
	if key, err := x509.ParsePKCS1PrivateKey(keyDER); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS8PrivateKey(keyDER); err == nil {
		switch key.(type) {
		case *rsa.PrivateKey, *ecdsa.PrivateKey, ed25519.PrivateKey:
			return key, nil
		default:
			return nil, errors.New("tls: found unknown private key type in PKCS#8 wrapping")
		}
	}
	if key, err := x509.ParseECPrivateKey(keyDER); err == nil {
		return key, nil
	}
	return nil, errors.New("tls: failed to parse private key")
}

// matchPublicAndPrivateKeys returns nil if pub belongs to priv. The checks
// mirror what crypto/tls.X509KeyPair performs internally.
func matchPublicAndPrivateKeys(pub, priv any) error {
	switch pub := pub.(type) {
	case *rsa.PublicKey:
		priv, ok := priv.(*rsa.PrivateKey)
		if !ok {
			return errors.New("tls: private key type does not match public key type")
		}
		if pub.N.Cmp(priv.N) != 0 {
			return errors.New("tls: private key does not match public key")
		}
	case *ecdsa.PublicKey:
		priv, ok := priv.(*ecdsa.PrivateKey)
		if !ok {
			return errors.New("tls: private key type does not match public key type")
		}
		if !pub.Equal(&priv.PublicKey) {
			return errors.New("tls: private key does not match public key")
		}
	case ed25519.PublicKey:
		priv, ok := priv.(ed25519.PrivateKey)
		if !ok {
			return errors.New("tls: private key type does not match public key type")
		}
		if !pub.Equal(priv.Public()) {
			return errors.New("tls: private key does not match public key")
		}
	default:
		return errors.New("tls: unsupported public key type")
	}
	return nil
}

func isNegativeSerialError(err error) bool {
	return err != nil && strings.Contains(err.Error(), negativeSerialErr)
}
