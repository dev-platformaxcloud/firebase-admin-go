// Copyright 2017 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package auth contains functions for minting custom authentication tokens, verifying Firebase ID tokens,
// and managing users in a Firebase project.
package auth

import (
	"context"

	"firebase.google.com/go/internal"
	"google.golang.org/api/identitytoolkit/v3"
	"google.golang.org/api/transport"
)

// Client is the interface for the Firebase auth service.
//
// Client facilitates generating custom JWT tokens for Firebase clients, and verifying ID tokens issued
// by Firebase backend services.
type Client struct {
	is              *identitytoolkit.Service
	idTokenVerifier *tokenVerifier
	tokenGenerator  *tokenGenerator
	version         string
}

// NewClient creates a new instance of the Firebase Auth Client.
//
// This function can only be invoked from within the SDK. Client applications should access the
// Auth service through firebase.App.
func NewClient(ctx context.Context, conf *internal.AuthConfig) (*Client, error) {
	hc, _, err := transport.NewHTTPClient(ctx, conf.Opts...)
	if err != nil {
		return nil, err
	}

	is, err := identitytoolkit.New(hc)
	if err != nil {
		return nil, err
	}

	tokenGenerator, err := newTokenGenerator(ctx, conf)
	if err != nil {
		return nil, err
	}

	idTokenVerifier, err := newIDTokenVerifier(ctx, conf.ProjectID)
	if err != nil {
		return nil, err
	}

	return &Client{
		is:              is,
		idTokenVerifier: idTokenVerifier,
		tokenGenerator:  tokenGenerator,
		version:         "Go/Admin/" + conf.Version,
	}, nil
}

// CustomToken creates a signed custom authentication token with the specified user ID.
//
// The resulting JWT can be used in a Firebase client SDK to trigger an authentication flow. See
// https://firebase.google.com/docs/auth/admin/create-custom-tokens#sign_in_using_custom_tokens_on_clients
// for more details on how to use custom tokens for client authentication.
//
// CustomToken follows the protocol outlined below to sign the generated tokens:
//   - If the SDK was initialized with service account credentials, uses the private key present in
//     the credentials to sign tokens locally.
//   - If a service account email was specified during initialization (via firebase.Config struct),
//     calls the IAM service with that email to sign tokens remotely. See
//     https://cloud.google.com/iam/reference/rest/v1/projects.serviceAccounts/signBlob.
//   - If the code is deployed in the Google App Engine standard environment, uses the App Identity
//     service to sign tokens. See https://cloud.google.com/appengine/docs/standard/go/reference#SignBytes.
//   - If the code is deployed in a different GCP-managed environment (e.g. Google Compute Engine),
//     uses the local Metadata server to auto discover a service account email. This is used in
//     conjunction with the IAM service to sign tokens remotely.
//
// CustomToken returns an error the SDK fails to discover a viable mechanism for signing tokens.
func (c *Client) CustomToken(ctx context.Context, uid string) (string, error) {
	return c.CustomTokenWithClaims(ctx, uid, nil)
}

// CustomTokenWithClaims is similar to CustomToken, but in addition to the user ID, it also encodes
// all the key-value pairs in the provided map as claims in the resulting JWT.
func (c *Client) CustomTokenWithClaims(ctx context.Context, uid string, devClaims map[string]interface{}) (string, error) {
	return c.tokenGenerator.Token(ctx, uid, devClaims)
}

// Token represents a decoded Firebase ID token.
//
// Token provides typed accessors to the common JWT fields such as Audience (aud) and Expiry (exp).
// Additionally it provides a UID field, which indicates the user ID of the account to which this token
// belongs. Any additional JWT claims can be accessed via the Claims map of Token.
type Token struct {
	Issuer   string                 `json:"iss"`
	Audience string                 `json:"aud"`
	Expires  int64                  `json:"exp"`
	IssuedAt int64                  `json:"iat"`
	Subject  string                 `json:"sub,omitempty"`
	UID      string                 `json:"uid,omitempty"`
	Claims   map[string]interface{} `json:"-"`
}

// VerifyIDToken verifies the signature	and payload of the provided ID token.
//
// VerifyIDToken accepts a signed JWT token string, and verifies that it is current, issued for the
// correct Firebase project, and signed by the Google Firebase services in the cloud. It returns
// a Token containing the decoded claims in the input JWT. See
// https://firebase.google.com/docs/auth/admin/verify-id-tokens#retrieve_id_tokens_on_clients for
// more details on how to obtain an ID token in a client app.
//
// This function does not make any RPC calls most of the time. The only time it makes an RPC call
// is when Google public keys need to be refreshed. These keys get cached up to 24 hours, and
// therefore the RPC overhead gets amortized over many invocations of this function.
//
// This does not check whether or not the token has been revoked. Use `VerifyIDTokenAndCheckRevoked()`
// when a revocation check is needed.
func (c *Client) VerifyIDToken(ctx context.Context, idToken string) (*Token, error) {
	return c.idTokenVerifier.VerifyToken(ctx, idToken)
}

// VerifyIDTokenAndCheckRevoked verifies the provided ID token, and additionally checks that the
// token has not been revoked.
//
// This function uses `VerifyIDToken()` internally to verify the ID token JWT. However, unlike
// `VerifyIDToken()` this function must make an RPC call to perform the revocation check.
// Developers are advised to take this additional overhead into consideration when including this
// function in an authorization flow that gets executed often.
func (c *Client) VerifyIDTokenAndCheckRevoked(ctx context.Context, idToken string) (*Token, error) {
	p, err := c.VerifyIDToken(ctx, idToken)
	if err != nil {
		return nil, err
	}

	user, err := c.GetUser(ctx, p.UID)
	if err != nil {
		return nil, err
	}

	if p.IssuedAt*1000 < user.TokensValidAfterMillis {
		return nil, internal.Error(idTokenRevoked, "ID token has been revoked")
	}
	return p, nil
}
