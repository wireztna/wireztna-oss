package controller

// Windows rename replacement is issued by os.Rename. Directory handles do not
// support the Unix directory fsync operation, so durability is established by
// syncing the replacement file before that atomic same-volume rename.
func syncJournalDirectory(string) error {
	return nil
}
