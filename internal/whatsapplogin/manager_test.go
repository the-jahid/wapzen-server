package whatsapplogin

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
)

func TestConfigureCompanionDeviceProps(t *testing.T) {
	originalOS := store.DeviceProps.Os
	originalPlatform := store.DeviceProps.PlatformType
	t.Cleanup(func() {
		store.DeviceProps.Os = originalOS
		store.DeviceProps.PlatformType = originalPlatform
	})

	configureCompanionDeviceProps()

	if got, want := store.DeviceProps.GetOs(), "Mac OS"; got != want {
		t.Fatalf("device OS = %q, want %q", got, want)
	}
	if got, want := store.DeviceProps.GetPlatformType(), waCompanionReg.DeviceProps_CHROME; got != want {
		t.Fatalf("device platform = %s, want %s", got, want)
	}
}
