package acme

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"

	"github.com/square/go-jose"
)

type jws struct {
	privKey *rsa.PrivateKey
	nonces  []string
}

func keyAsJWK(key interface{}) *jose.JsonWebKey {
	switch k := key.(type) {
	case *ecdsa.PublicKey:
		return &jose.JsonWebKey{Key: k, Algorithm: "EC"}
	case *rsa.PublicKey:
		return &jose.JsonWebKey{Key: k, Algorithm: "RSA"}

	default:
		return nil
	}
}

// Posts a JWS signed message to the specified URL
func (j *jws) post(url string, content []byte) (*http.Response, error) {
	if len(j.nonces) == 0 {
		err := j.getNonce(url)
		if err != nil {
			return nil, fmt.Errorf("Could not get a nonce for request: %s\n\t\tError: %v", url, err)
		}
	}

	signedContent, err := j.signContent(content)
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(url, "application/jose+json", bytes.NewBuffer([]byte(signedContent.FullSerialize())))
	if err != nil {
		return nil, err
	}

	j.getNonceFromResponse(resp)

	return resp, err
}

func (j *jws) signContent(content []byte) (*jose.JsonWebSignature, error) {
	// TODO: support other algorithms - RS512
	signer, err := jose.NewSigner(jose.RS256, j.privKey)
	if err != nil {
		return nil, err
	}
	signer.SetNonceSource(j)

	signed, err := signer.Sign(content)
	if err != nil {
		return nil, err
	}
	return signed, nil
}

func (j *jws) getNonceFromResponse(resp *http.Response) error {
	nonce := resp.Header.Get("Replay-Nonce")
	if nonce == "" {
		return fmt.Errorf("Server did not respond with a proper nonce header.")
	}

	j.nonces = append(j.nonces, nonce)
	return nil
}

func (j *jws) getNonce(url string) error {
	resp, err := http.Head(url)
	if err != nil {
		return err
	}

	return j.getNonceFromResponse(resp)
}

func (j *jws) Nonce() (string, error) {
	nonce := ""
	if len(j.nonces) == 0 {
		return nonce, errors.New("No nonce available.")
	}

	nonce, j.nonces = j.nonces[len(j.nonces)-1], j.nonces[:len(j.nonces)-1]
	return nonce, nil
}
