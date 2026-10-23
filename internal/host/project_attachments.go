package host

// WithProjectAttachmentWrite keeps this Host's directory lease alive during a
// short mutation of the independent media/ attachment tree. Unlike source
// snapshots, attachments do not touch planning, chapter prose, usage, or engine
// state, so they can be saved while a model is generating. The caller must
// serialize project switching/copying and validate image data before entering.
// The callback must not call Host methods or mutate any non-attachment files.
func (h *Host) WithProjectAttachmentWrite(write func() error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing || h.bookLease == nil || h.bookLease.lock == nil || !h.bookLease.lock.Locked() || write == nil {
		return ErrProjectSnapshotBusy
	}
	return write()
}
