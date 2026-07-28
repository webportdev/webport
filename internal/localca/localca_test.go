package localca

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureCreatesAndReusesCA(t *testing.T) {
	dir := t.TempDir()
	first, err := Ensure(dir, "webport.localhost")
	if err != nil {
		t.Fatal(err)
	}
	if !first.CreatedCA {
		t.Fatal("first Ensure did not report a new CA")
	}
	if !first.Changed {
		t.Fatal("first Ensure did not report changed material")
	}
	caBefore, err := os.ReadFile(first.CACert)
	if err != nil {
		t.Fatal(err)
	}

	second, err := Ensure(dir, "webport.localhost")
	if err != nil {
		t.Fatal(err)
	}
	if second.CreatedCA {
		t.Fatal("second Ensure unexpectedly replaced the CA")
	}
	if second.Changed {
		t.Fatal("second Ensure unexpectedly changed certificate material")
	}
	caAfter, err := os.ReadFile(second.CACert)
	if err != nil {
		t.Fatal(err)
	}
	if string(caBefore) != string(caAfter) {
		t.Fatal("CA changed between calls")
	}

	caCert, err := readCertificate(first.CACert)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := readCertificate(first.Cert)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "app.webport.localhost"}); err != nil {
		t.Fatalf("verify wildcard certificate: %v", err)
	}
}

func TestEnsureRegeneratesLeafForChangedDomain(t *testing.T) {
	dir := t.TempDir()
	first, err := Ensure(dir, "one.localhost")
	if err != nil {
		t.Fatal(err)
	}
	oldCA, err := os.ReadFile(first.CACert)
	if err != nil {
		t.Fatal(err)
	}

	second, err := Ensure(dir, "two.localhost")
	if err != nil {
		t.Fatal(err)
	}
	if !second.CreatedCA {
		t.Fatal("domain change must create a constrained replacement CA")
	}
	newCA, err := os.ReadFile(second.CACert)
	if err != nil {
		t.Fatal(err)
	}
	if string(oldCA) == string(newCA) {
		t.Fatal("domain change reused the old CA")
	}
	leaf, err := readCertificate(second.Cert)
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.VerifyHostname("app.two.localhost"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRepairsPermissions(t *testing.T) {
	paths, err := Ensure(t.TempDir(), "webport.localhost")
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		paths.CACert: 0644,
		paths.CAKey:  0600,
		paths.Cert:   0644,
		paths.Key:    0640,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s permissions = %o, want %o", filepath.Base(path), got, want)
		}
	}
}
