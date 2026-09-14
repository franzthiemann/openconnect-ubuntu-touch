package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// The CA is generated once and then reused forever. That matters for usability
// rather than security: the user has to pick ca.crt by hand in the Settings VPN
// editor, and rotating the CA would silently break that connection and force
// them to do it again.
const caValidity = 20 * 365 * 24 * time.Hour

// RSA-2048 rather than an EC key: this certificate is consumed by whatever
// openvpn and NetworkManager version the device's rootfs happens to ship, and
// RSA is the safest common denominator. It is generated once, so the cost is
// paid on first run only.
const rsaBits = 2048

func writeFile(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func pemBlock(path, typ string, der []byte, perm os.FileMode) error {
	return writeFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), perm)
}

func serial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

// EnsurePKI creates the CA and server certificate under dir if they are not
// already there, and returns their paths. Existing material is left alone.
func EnsurePKI(dir string) (Paths, error) {
	p := Paths{
		CACert:     filepath.Join(dir, "ca.crt"),
		ServerCert: filepath.Join(dir, "server.crt"),
		ServerKey:  filepath.Join(dir, "server.key"),
	}
	caKeyPath := filepath.Join(dir, "ca.key")

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return p, err
	}
	complete := true
	for _, f := range []string{p.CACert, p.ServerCert, p.ServerKey, caKeyPath} {
		if _, err := os.Stat(f); err != nil {
			complete = false
			break
		}
	}
	if complete {
		return p, nil
	}

	now := time.Now().Add(-1 * time.Hour) // tolerate a skewed device clock
	caKey, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return p, err
	}
	sn, err := serial()
	if err != nil {
		return p, err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: "ocbridge local CA"},
		NotBefore:             now,
		NotAfter:              now.Add(caValidity),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return p, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return p, err
	}

	srvKey, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return p, err
	}
	if sn, err = serial(); err != nil {
		return p, err
	}
	srvTmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: "ocbridge local server"},
		NotBefore:    now,
		NotAfter:     now.Add(caValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		// openvpn clients configured with --remote-cert-tls server require this
		// EKU; NetworkManager sets that when "Server certificate type" is used,
		// so include it unconditionally.
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, caCert, &srvKey.PublicKey, caKey)
	if err != nil {
		return p, err
	}

	if err := pemBlock(p.CACert, "CERTIFICATE", caDER, 0o644); err != nil {
		return p, err
	}
	if err := pemBlock(caKeyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(caKey), 0o600); err != nil {
		return p, err
	}
	if err := pemBlock(p.ServerCert, "CERTIFICATE", srvDER, 0o644); err != nil {
		return p, err
	}
	if err := pemBlock(p.ServerKey, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(srvKey), 0o600); err != nil {
		return p, err
	}
	return p, nil
}

// EnsureCreds returns the username and password the local OpenVPN client must
// use, generating them once. They are shown to the user by the app's setup page
// so they can be typed into the Settings VPN editor.
func EnsureCreds(path string) (user, pass string, err error) {
	if b, e := os.ReadFile(path); e == nil {
		lines := splitLines(string(b))
		if len(lines) >= 2 && lines[0] != "" && lines[1] != "" {
			return lines[0], lines[1], nil
		}
	}
	if user, err = token(6); err != nil {
		return "", "", err
	}
	if pass, err = token(15); err != nil {
		return "", "", err
	}
	user = "ubt-" + user
	if err = writeFile(path, []byte(user+"\n"+pass+"\n"), 0o600); err != nil {
		return "", "", err
	}
	return user, pass, nil
}

// token returns n characters from an unambiguous alphabet -- the user retypes
// these into a phone keyboard, so 0/O and 1/l/I are excluded.
func token(n int) (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out), nil
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		if r != '\r' {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
