//go:build !unpack

package library

// Without the unpack feature, archives are left in import/ as they are,
// and can't be uploaded.

const unpackBuilt = false

func isArchive(string) bool                                 { return false }
func archiveFirst(string) bool                              { return false }
func archiveFirstVolume(name string) string                 { return name }
func ArchiveExts() []string                                 { return nil }
func (s *Importer) unpackLoose(_, _ string) (string, error) { return "", nil }

func (s *Importer) unpackItem(_, _ string, _ []string, _ unpacked) error { return nil }
