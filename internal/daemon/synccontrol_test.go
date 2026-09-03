package daemon

import "testing"

func TestSyncControlPersistsPauseAndBandwidth(t *testing.T) {
	root := t.TempDir()
	control, err := OpenSyncControl(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	if err := control.SetBandwidth(4096); err != nil {
		t.Fatal(err)
	}
	reopened, _ := OpenSyncControl(root)
	value, err := reopened.Load()
	if err != nil || !value.Paused || value.BandwidthBytesPerSecond != 4096 {
		t.Fatalf("settings=%+v err=%v", value, err)
	}
	if err := reopened.SetBandwidth(-1); err == nil {
		t.Fatal("negative bandwidth limit accepted")
	}
}
