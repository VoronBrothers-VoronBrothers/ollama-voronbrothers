package gguf

import "testing"

// The sizes are the ones the published Bonsai 2 files lay out: output.weight
// is 5120 x 248320 weights, and the next tensor starts where it ends.
func TestATernaryTensorHasTheSizeItsFileGivesIt(t *testing.T) {
	const weights = 5120 * 248320
	for _, tc := range []struct {
		tt   TensorType
		name string
		want uint64
	}{
		{TensorTypePQ2_0, "pq2_0", 337715200},
		{TensorTypePTQ1_0, "ptq1_0", 278118400},
	} {
		info := TensorInfo{Name: "output.weight", Shape: []uint64{5120, 248320}, Type: tc.tt}
		got, ok := info.numBytes()
		if !ok || uint64(got) != tc.want {
			t.Errorf("%s: %d weights take %d bytes (ok=%v), the file says %d", tc.name, weights, got, ok, tc.want)
		}
		if tc.tt.String() != tc.name {
			t.Errorf("type %d is named %q, want %q", uint32(tc.tt), tc.tt.String(), tc.name)
		}
	}
}

func TestATernaryFileTypeIsNamed(t *testing.T) {
	for ft, want := range map[FileType]string{141: "PQ2_0", 142: "PQ2_0", 143: "PTQ1_0", 999: "unknown"} {
		if got := ft.String(); got != want {
			t.Errorf("file type %d is %q, want %q", ft, got, want)
		}
	}
}

// A type nobody lists keeps upstream's answer: no size, so the file is refused.
func TestAnUnlistedTypeStillHasNoSize(t *testing.T) {
	if got := TensorType(200).typeSize(); got != 0 {
		t.Errorf("type 200 has size %d, want 0", got)
	}
	if got := TensorType(200).String(); got != "unknown" {
		t.Errorf("type 200 is named %q", got)
	}
}
