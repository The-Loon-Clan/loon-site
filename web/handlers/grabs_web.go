package handlers

// Grab counting — the missing feature the parity list called out.
//
// Nothing recorded NZB downloads, which blocked three separate things at once:
// the economy plugin (its entire job is a per-grab uploader bonus, and
// UploaderGrabTotals had no source), UNIT3D's trending pages, and the "N
// downloads" figure every UNIT3D listing shows.
//
// Deliberately NOT mocked while it was missing — a faked download count would
// have corrupted the very features that were waiting to read it.
