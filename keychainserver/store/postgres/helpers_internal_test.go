package postgres

import "testing"

func TestNonNilStrings(t *testing.T) {
	if got := nonNilStrings(nil); got == nil || len(got) != 0 {
		t.Fatalf("nonNilStrings(nil) = %#v, want empty non-nil slice", got)
	}
	in := []string{"read"}
	got := nonNilStrings(in)
	if len(got) != 1 || got[0] != "read" {
		t.Fatalf("nonNilStrings = %v, want [read]", got)
	}
}

func TestUnmarshalMetadataEmptyAndInvalid(t *testing.T) {
	if got := unmarshalMetadata(nil); got != nil {
		t.Fatalf("unmarshalMetadata(nil) = %v, want nil", got)
	}
	if got := unmarshalMetadata([]byte(`{}`)); got != nil {
		t.Fatalf("unmarshalMetadata({}) = %v, want nil", got)
	}
	if got := unmarshalMetadata([]byte(`not json`)); got != nil {
		t.Fatalf("unmarshalMetadata(invalid) = %v, want nil", got)
	}
}
