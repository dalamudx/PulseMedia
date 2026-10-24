package app

import "testing"

func TestDownloadWatchLibraryIdentityBinding(t *testing.T) {
	binding := watchBinding()
	binding.SourceKind = "library_identity"
	binding.LocalRoot = binding.RootPath
	binding.RemotePrefix = ""
	if !validDownloadWatchBinding(binding) {
		t.Fatal("configured local library identity incorrectly requires a remote prefix")
	}
	binding.RemotePrefix = "/remote"
	if validDownloadWatchBinding(binding) {
		t.Fatal("identity source accepted a remote mapping prefix")
	}
	binding.SourceKind = "path_mapping"
	if !validDownloadWatchBinding(binding) {
		t.Fatal("explicit remote mapping rejected")
	}
	binding.RemotePrefix = ""
	if validDownloadWatchBinding(binding) {
		t.Fatal("remote mapping accepted an absent prefix")
	}
}
