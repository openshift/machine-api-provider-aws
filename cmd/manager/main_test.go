package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func generateSelfSignedCert(dir string) (certFile, keyFile string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}

	certFile = filepath.Join(dir, "tls.crt")
	keyFile = filepath.Join(dir, "tls.key")

	certOut, err := os.Create(certFile)
	if err != nil {
		return "", "", err
	}
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	certOut.Close()

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	keyOut, err := os.Create(keyFile)
	if err != nil {
		return "", "", err
	}
	pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	keyOut.Close()

	return certFile, keyFile, nil
}

func TestPprofServerResponds(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, err := generateSelfSignedCert(dir)
	if err != nil {
		t.Fatalf("failed to generate test cert: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	srv := &pprofServer{
		Addr:     addr,
		CertFile: certFile,
		KeyFile:  keyFile,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	var resp *http.Response
	for i := 0; i < 20; i++ {
		resp, err = client.Get(fmt.Sprintf("https://%s/debug/pprof/", addr))
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("pprof server did not respond: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("server returned error on shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not shut down within 10 seconds")
	}
}

func TestPprofServerGracefulShutdown(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, err := generateSelfSignedCert(dir)
	if err != nil {
		t.Fatalf("failed to generate test cert: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	srv := &pprofServer{
		Addr:     addr,
		CertFile: certFile,
		KeyFile:  keyFile,
	}

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	// Wait for the server to be ready
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	for i := 0; i < 20; i++ {
		resp, getErr := client.Get(fmt.Sprintf("https://%s/debug/pprof/", addr))
		if getErr == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("server returned error on shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down within 5 seconds")
	}
}

func TestPprofServerNeedLeaderElection(t *testing.T) {
	srv := &pprofServer{}
	if srv.NeedLeaderElection() {
		t.Error("pprofServer.NeedLeaderElection() returned true, expected false")
	}
}
