package model

import (
	"strings"
	"testing"
)

func TestAdminLogBeforeCreateSanitizesNestedJSON(t *testing.T) {
	body := `{"username":"rolling","password":"secret-pass","nested":{"api_key":"k-123","items":[{"access_token":"tok-456"}]}}`
	log := &AdminLog{
		URL:  "/admin/login",
		Data: &body,
	}
	if err := log.BeforeCreate(nil); err != nil {
		t.Fatalf("BeforeCreate error: %v", err)
	}
	if log.Data == nil {
		t.Fatal("expected non-nil Data")
	}
	for _, secret := range []string{"secret-pass", "k-123", "tok-456"} {
		if strings.Contains(*log.Data, secret) {
			t.Fatalf("sensitive value %q leaked in AdminLog.Data: %s", secret, *log.Data)
		}
	}
	if log.Username != "rolling" {
		t.Fatalf("expected Username=rolling, got %q", log.Username)
	}
}
