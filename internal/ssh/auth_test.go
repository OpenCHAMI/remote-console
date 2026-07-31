// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	compcredentials "github.com/Cray-HPE/hms-compcredentials"
	gossh "golang.org/x/crypto/ssh"
)

func writeTestPrivateKey(t *testing.T, path string) gossh.Signer {
	t.Helper()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := gossh.MarshalPrivateKey(privateKey, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func writeTestCertificate(t *testing.T, path string, key gossh.PublicKey) {
	t.Helper()

	_, caPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caSigner, err := gossh.NewSignerFromKey(caPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	cert := &gossh.Certificate{
		Key:         key,
		CertType:    gossh.UserCert,
		KeyId:       "test",
		ValidBefore: gossh.CertTimeInfinity,
	}
	if err := cert.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, gossh.MarshalAuthorizedKey(cert), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildAuth(t *testing.T) {
	t.Run("password takes priority", func(t *testing.T) {
		console := &SSHConsole{nodeID: "node", keyPath: filepath.Join(t.TempDir(), "missing")}
		methods, err := console.buildAuth(compcredentials.CompCredentials{Password: "password"})
		if err != nil {
			t.Fatal(err)
		}
		if len(methods) != 1 {
			t.Fatalf("got %d auth methods, want 1", len(methods))
		}
	})

	t.Run("missing key path", func(t *testing.T) {
		console := &SSHConsole{nodeID: "node"}
		_, err := console.buildAuth(compcredentials.CompCredentials{})
		if err == nil || !strings.Contains(err.Error(), "no key path configured") {
			t.Fatalf("got %v, want missing key path error", err)
		}
	})

	t.Run("missing key file", func(t *testing.T) {
		console := &SSHConsole{nodeID: "node", keyPath: filepath.Join(t.TempDir(), "missing")}
		_, err := console.buildAuth(compcredentials.CompCredentials{})
		if err == nil || !strings.Contains(err.Error(), "read SSH key") {
			t.Fatalf("got %v, want key read error", err)
		}
	})

	t.Run("invalid private key", func(t *testing.T) {
		keyPath := filepath.Join(t.TempDir(), "console.key")
		if err := os.WriteFile(keyPath, []byte("invalid"), 0600); err != nil {
			t.Fatal(err)
		}
		console := &SSHConsole{nodeID: "node", keyPath: keyPath}
		_, err := console.buildAuth(compcredentials.CompCredentials{})
		if err == nil || !strings.Contains(err.Error(), "parse SSH private key") {
			t.Fatalf("got %v, want private key parse error", err)
		}
	})

	t.Run("key only", func(t *testing.T) {
		keyPath := filepath.Join(t.TempDir(), "console.key")
		writeTestPrivateKey(t, keyPath)
		console := &SSHConsole{nodeID: "node", keyPath: keyPath}
		methods, err := console.buildAuth(compcredentials.CompCredentials{})
		if err != nil {
			t.Fatal(err)
		}
		if len(methods) != 1 {
			t.Fatalf("got %d auth methods, want 1", len(methods))
		}
	})

	t.Run("invalid certificate", func(t *testing.T) {
		keyPath := filepath.Join(t.TempDir(), "console.key")
		writeTestPrivateKey(t, keyPath)
		if err := os.WriteFile(keyPath+"-cert.pub", []byte("invalid"), 0644); err != nil {
			t.Fatal(err)
		}
		console := &SSHConsole{nodeID: "node", keyPath: keyPath}
		_, err := console.buildAuth(compcredentials.CompCredentials{})
		if err == nil || !strings.Contains(err.Error(), "parse SSH certificate") {
			t.Fatalf("got %v, want certificate parse error", err)
		}
	})

	t.Run("public key is not a certificate", func(t *testing.T) {
		keyPath := filepath.Join(t.TempDir(), "console.key")
		signer := writeTestPrivateKey(t, keyPath)
		if err := os.WriteFile(keyPath+"-cert.pub", gossh.MarshalAuthorizedKey(signer.PublicKey()), 0644); err != nil {
			t.Fatal(err)
		}
		console := &SSHConsole{nodeID: "node", keyPath: keyPath}
		_, err := console.buildAuth(compcredentials.CompCredentials{})
		if err == nil || !strings.Contains(err.Error(), "is not an SSH certificate") {
			t.Fatalf("got %v, want certificate type error", err)
		}
	})

	t.Run("certificate key mismatch", func(t *testing.T) {
		keyPath := filepath.Join(t.TempDir(), "console.key")
		writeTestPrivateKey(t, keyPath)
		_, otherPrivateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		otherSigner, err := gossh.NewSignerFromKey(otherPrivateKey)
		if err != nil {
			t.Fatal(err)
		}
		writeTestCertificate(t, keyPath+"-cert.pub", otherSigner.PublicKey())

		console := &SSHConsole{nodeID: "node", keyPath: keyPath}
		_, err = console.buildAuth(compcredentials.CompCredentials{})
		if err == nil || !strings.Contains(err.Error(), "create cert signer") {
			t.Fatalf("got %v, want certificate signer error", err)
		}
	})

	t.Run("certificate", func(t *testing.T) {
		keyPath := filepath.Join(t.TempDir(), "console.key")
		signer := writeTestPrivateKey(t, keyPath)
		writeTestCertificate(t, keyPath+"-cert.pub", signer.PublicKey())

		console := &SSHConsole{nodeID: "node", keyPath: keyPath}
		methods, err := console.buildAuth(compcredentials.CompCredentials{})
		if err != nil {
			t.Fatal(err)
		}
		if len(methods) != 1 {
			t.Fatalf("got %d auth methods, want 1", len(methods))
		}
	})
}
