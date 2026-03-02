package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/jimeng-relay/server/internal/config"
	apikeyservice "github.com/jimeng-relay/server/internal/service/apikey"
)

func TestLegacyCompatibility_CLIKeyLifecycle(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "jimeng-relay-test.db")

	encKeyRaw := []byte("0123456789abcdef0123456789abcdef")
	encKeyB64 := base64.StdEncoding.EncodeToString(encKeyRaw)

	t.Setenv(config.EnvDatabaseType, "sqlite")
	t.Setenv(config.EnvDatabaseURL, dbPath)
	t.Setenv(config.EnvAPIKeyEncryptionKey, encKeyB64)

	ctxOut := func(args []string) string {
		var out bytes.Buffer
		if err := run(args, &out); err != nil {
			t.Fatalf("run %v: %v\noutput=%s", args, err, out.String())
		}
		return out.String()
	}

	var created apikeyservice.KeyWithSecret
	if err := json.Unmarshal([]byte(ctxOut([]string{"key", "create", "--description", "legacy"})), &created); err != nil {
		t.Fatalf("unmarshal key create: %v", err)
	}
	if created.ID == "" || created.AccessKey == "" || created.SecretKey == "" {
		t.Fatalf("expected create to return id/access_key/secret_key, got: %#v", created)
	}

	var listed struct {
		Items []apikeyservice.KeyView `json:"items"`
	}
	if err := json.Unmarshal([]byte(ctxOut([]string{"key", "list"})), &listed); err != nil {
		t.Fatalf("unmarshal key list: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != created.ID {
		t.Fatalf("expected list to include created key, got: %#v", listed)
	}

	var revoked map[string]any
	if err := json.Unmarshal([]byte(ctxOut([]string{"key", "revoke", "--id", created.ID})), &revoked); err != nil {
		t.Fatalf("unmarshal key revoke: %v", err)
	}
	if revoked["id"] != created.ID || revoked["status"] != "revoked" {
		t.Fatalf("unexpected revoke output: %#v", revoked)
	}

	var toRotate apikeyservice.KeyWithSecret
	if err := json.Unmarshal([]byte(ctxOut([]string{"key", "create", "--description", "rotate-me"})), &toRotate); err != nil {
		t.Fatalf("unmarshal key create (rotate target): %v", err)
	}

	var rotated apikeyservice.KeyWithSecret
	if err := json.Unmarshal([]byte(ctxOut([]string{"key", "rotate", "--id", toRotate.ID, "--description", "rotated", "--grace-period", "0s"})), &rotated); err != nil {
		t.Fatalf("unmarshal key rotate: %v", err)
	}
	if rotated.ID == "" || rotated.AccessKey == "" || rotated.SecretKey == "" {
		t.Fatalf("expected rotate to return new id/access_key/secret_key, got: %#v", rotated)
	}
	if rotated.RotationOf == nil || *rotated.RotationOf != toRotate.ID {
		t.Fatalf("expected rotation_of to point old key id, got: %#v", rotated.RotationOf)
	}
}
