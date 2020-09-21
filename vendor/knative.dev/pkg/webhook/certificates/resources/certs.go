/*
Copyright 2019 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package resources

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"time"

	"go.uber.org/zap"

	"knative.dev/pkg/logging"
)

const (
	organization = "knative.dev"
)

// Create the common parts of the cert. These don't change between
// the root/CA cert and the server cert.
func createCertTemplate(name, namespace string, notAfter time.Time) (*x509.Certificate, error) {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, errors.New("failed to generate serial number: " + err.Error())
	}

	serviceName := name + "." + namespace
	commonName := serviceName + ".svc"
	serviceNames := []string{
		name,
		serviceName,
		commonName,
		serviceName + ".svc.cluster.local",
	}

	tmpl := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{organization},
			CommonName:   commonName,
		},
		SignatureAlgorithm:    x509.SHA256WithRSA,
		NotBefore:             time.Now(),
		NotAfter:              notAfter,
		BasicConstraintsValid: true,
		DNSNames:              serviceNames,
	}
	return &tmpl, nil
}

// Create cert template suitable for CA and hence signing
func createCACertTemplate(name, namespace string, notAfter time.Time) (*x509.Certificate, error) {
	rootCert, err := createCertTemplate(name, namespace, notAfter)
	if err != nil {
		return nil, err
	}
	// Make it into a CA cert and change it so we can use it to sign certs
	rootCert.IsCA = true
	rootCert.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature
	rootCert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	return rootCert, nil
}

// Create cert template that we can use on the server for TLS
func createServerCertTemplate(name, namespace string, notAfter time.Time) (*x509.Certificate, error) {
	serverCert, err := createCertTemplate(name, namespace, notAfter)
	if err != nil {
		return nil, err
	}
	serverCert.KeyUsage = x509.KeyUsageDigitalSignature
	serverCert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	return serverCert, err
}

// Actually sign the cert and return things in a form that we can use later on
func createCert(template, parent *x509.Certificate, pub, parentPriv interface{}) (
	cert *x509.Certificate, certPEM []byte, err error) {

	certDER, err := x509.CreateCertificate(rand.Reader, template, parent, pub, parentPriv)
	if err != nil {
		return
	}
	cert, err = x509.ParseCertificate(certDER)
	if err != nil {
		return
	}
	b := pem.Block{Type: "CERTIFICATE", Bytes: certDER}
	certPEM = pem.EncodeToMemory(&b)
	return
}

func createCA(ctx context.Context, name, namespace string, notAfter time.Time) (*rsa.PrivateKey, *x509.Certificate, []byte, error) {
	logger := logging.FromContext(ctx)
	rootKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		logger.Errorw("error generating random key", zap.Error(err))
		return nil, nil, nil, err
	}

	rootCertTmpl, err := createCACertTemplate(name, namespace, notAfter)
	if err != nil {
		logger.Errorw("error generating CA cert", zap.Error(err))
		return nil, nil, nil, err
	}

	rootCert, rootCertPEM, err := createCert(rootCertTmpl, rootCertTmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		logger.Errorw("error signing the CA cert", zap.Error(err))
		return nil, nil, nil, err
	}
	return rootKey, rootCert, rootCertPEM, nil
}

// CreateCerts creates and returns a CA certificate and certificate and
// key for the server. serverKey and serverCert are used by the server
// to establish trust for clients, CA certificate is used by the
// client to verify the server authentication chain. notAfter specifies
// the expiration date.
func CreateCerts(ctx context.Context, name, namespace string, notAfter time.Time) (serverKey, serverCert, caCert []byte, err error) {
	logger := logging.FromContext(ctx)
	// First create a CA certificate and private key
	caKey, caCertificate, caCertificatePEM, err := createCA(ctx, name, namespace, notAfter)
	if err != nil {
		return nil, nil, nil, err
	}

	// Then create the private key for the serving cert
	servKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		logger.Errorw("error generating random key", zap.Error(err))
		return nil, nil, nil, err
	}
	servCertTemplate, err := createServerCertTemplate(name, namespace, notAfter)
	if err != nil {
		logger.Errorw("failed to create the server certificate template", zap.Error(err))
		return nil, nil, nil, err
	}

	// create a certificate which wraps the server's public key, sign it with the CA private key
	_, servCertPEM, err := createCert(servCertTemplate, caCertificate, &servKey.PublicKey, caKey)
	if err != nil {
		logger.Errorw("error signing server certificate template", zap.Error(err))
		return nil, nil, nil, err
	}
	servKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(servKey),
	})
	return servKeyPEM, servCertPEM, caCertificatePEM, nil
}
