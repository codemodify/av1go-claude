package encoder

import (
	"av1go/obu"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig(1920, 1080)

	if cfg.Width != 1920 {
		t.Fatalf("expected width 1920, got %d", cfg.Width)
	}
	if cfg.Height != 1080 {
		t.Fatalf("expected height 1080, got %d", cfg.Height)
	}
	if cfg.Profile != ProfileMain {
		t.Fatalf("expected ProfileMain, got %d", cfg.Profile)
	}
	if cfg.BitDepth != 8 {
		t.Fatalf("expected 8-bit, got %d", cfg.BitDepth)
	}
	if cfg.PixelFormat != PixelFormatYUV420 {
		t.Fatalf("expected YUV420, got %v", cfg.PixelFormat)
	}
	if cfg.RateControl != RateControlCQ {
		t.Fatalf("expected CQ, got %v", cfg.RateControl)
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*Config)
		wantErr bool
	}{
		{
			name:    "valid default",
			modify:  func(c *Config) {},
			wantErr: false,
		},
		{
			name:    "zero width",
			modify:  func(c *Config) { c.Width = 0 },
			wantErr: true,
		},
		{
			name:    "zero height",
			modify:  func(c *Config) { c.Height = 0 },
			wantErr: true,
		},
		{
			name:    "oversized width",
			modify:  func(c *Config) { c.Width = 70000 },
			wantErr: true,
		},
		{
			name:    "invalid bit depth",
			modify:  func(c *Config) { c.BitDepth = 16 },
			wantErr: true,
		},
		{
			name:    "invalid profile",
			modify:  func(c *Config) { c.Profile = 5 },
			wantErr: true,
		},
		{
			name:    "main profile 12-bit",
			modify:  func(c *Config) { c.BitDepth = 12 },
			wantErr: true,
		},
		{
			name: "main profile YUV444",
			modify: func(c *Config) {
				c.PixelFormat = PixelFormatYUV444
			},
			wantErr: true,
		},
		{
			name: "high profile YUV420",
			modify: func(c *Config) {
				c.Profile = ProfileHigh
				c.PixelFormat = PixelFormatYUV420
			},
			wantErr: true,
		},
		{
			name: "high profile YUV444",
			modify: func(c *Config) {
				c.Profile = ProfileHigh
				c.PixelFormat = PixelFormatYUV444
			},
			wantErr: false,
		},
		{
			name: "professional profile 12-bit",
			modify: func(c *Config) {
				c.Profile = ProfileProfessional
				c.BitDepth = 12
				c.PixelFormat = PixelFormatYUV422
			},
			wantErr: false,
		},
		{
			name:    "zero frame rate",
			modify:  func(c *Config) { c.FrameRateNum = 0 },
			wantErr: true,
		},
		{
			name:    "QP too high",
			modify:  func(c *Config) { c.QP = 64 },
			wantErr: true,
		},
		{
			name:    "negative QP",
			modify:  func(c *Config) { c.QP = -1 },
			wantErr: true,
		},
		{
			name:    "speed too high",
			modify:  func(c *Config) { c.SpeedLevel = 7 },
			wantErr: true,
		},
		{
			name: "CBR without bitrate",
			modify: func(c *Config) {
				c.RateControl = RateControlCBR
				c.TargetBitrate = 0
			},
			wantErr: true,
		},
		{
			name: "CBR with bitrate",
			modify: func(c *Config) {
				c.RateControl = RateControlCBR
				c.TargetBitrate = 4000
			},
			wantErr: false,
		},
		{
			name:    "tile columns too high",
			modify:  func(c *Config) { c.TileColumns = 7 },
			wantErr: true,
		},
		{
			name:    "main profile monochrome",
			modify:  func(c *Config) { c.PixelFormat = PixelFormatMonochrome },
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig(1920, 1080)
			tt.modify(&cfg)
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfigSequenceHeader(t *testing.T) {
	cfg := DefaultConfig(1920, 1080)
	sh := cfg.SequenceHeader()

	if sh.SeqProfile != ProfileMain {
		t.Fatalf("expected ProfileMain, got %d", sh.SeqProfile)
	}
	if sh.MaxFrameWidth() != 1920 {
		t.Fatalf("expected max width 1920, got %d", sh.MaxFrameWidth())
	}
	if sh.MaxFrameHeight() != 1080 {
		t.Fatalf("expected max height 1080, got %d", sh.MaxFrameHeight())
	}
	if !sh.EnableCDEF {
		t.Fatal("expected CDEF enabled")
	}
	if !sh.EnableRestoration {
		t.Fatal("expected restoration enabled")
	}
	if sh.ColorConfig.BitDepth() != 8 {
		t.Fatalf("expected 8-bit, got %d", sh.ColorConfig.BitDepth())
	}
	if sh.ColorConfig.ColorPrimaries != obu.ColorPrimariesBT709 {
		t.Fatalf("expected BT709 primaries, got %d", sh.ColorConfig.ColorPrimaries)
	}

	// Should be serializable.
	data, err := obu.WriteSequenceHeader(sh)
	if err != nil {
		t.Fatalf("failed to serialize sequence header: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty sequence header data")
	}
}

func TestConfigSequenceHeader10Bit(t *testing.T) {
	cfg := DefaultConfig(3840, 2160)
	cfg.Profile = ProfileHigh
	cfg.BitDepth = 10
	cfg.PixelFormat = PixelFormatYUV444
	sh := cfg.SequenceHeader()

	if !sh.ColorConfig.HighBitDepth {
		t.Fatal("expected HighBitDepth=true")
	}
	if sh.ColorConfig.BitDepth() != 10 {
		t.Fatalf("expected 10-bit, got %d", sh.ColorConfig.BitDepth())
	}
}

func TestConfigSequenceHeaderStillPicture(t *testing.T) {
	cfg := DefaultConfig(4096, 4096)
	cfg.StillPicture = true
	sh := cfg.SequenceHeader()

	if !sh.StillPicture {
		t.Fatal("expected StillPicture=true")
	}
	if !sh.ReducedStillPictureHeader {
		t.Fatal("expected ReducedStillPictureHeader=true")
	}
	if sh.TimingInfoPresent {
		t.Fatal("expected no timing info for still picture")
	}

	data, err := obu.WriteSequenceHeader(sh)
	if err != nil {
		t.Fatalf("failed to serialize: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty data")
	}
}

func TestConfigSequenceHeaderMonochrome(t *testing.T) {
	cfg := DefaultConfig(1920, 1080)
	cfg.PixelFormat = PixelFormatMonochrome
	sh := cfg.SequenceHeader()

	if !sh.ColorConfig.MonoChrome {
		t.Fatal("expected MonoChrome=true")
	}
}

func TestRateControlModeString(t *testing.T) {
	if RateControlCQ.String() != "CQ" {
		t.Fatalf("expected CQ, got %s", RateControlCQ.String())
	}
	if RateControlCBR.String() != "CBR" {
		t.Fatalf("expected CBR, got %s", RateControlCBR.String())
	}
	if RateControlVBR.String() != "VBR" {
		t.Fatalf("expected VBR, got %s", RateControlVBR.String())
	}
}

func TestPixelFormatString(t *testing.T) {
	if PixelFormatYUV420.String() != "YUV420" {
		t.Fatalf("expected YUV420, got %s", PixelFormatYUV420.String())
	}
	if PixelFormatMonochrome.String() != "Monochrome" {
		t.Fatalf("expected Monochrome, got %s", PixelFormatMonochrome.String())
	}
}
