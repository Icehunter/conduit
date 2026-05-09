package pendingedits

import "errors"

// errEmptyPath is returned when Stage is called with an empty Entry.Path.
var errEmptyPath = errors.New("pendingedits: empty path")

// ErrNotStaging is returned by GatedStager.Stage when the current permission
// mode does not require staging. File tools treat this sentinel as "proceed
// with the direct-write path instead of returning a staged result".
var ErrNotStaging = errors.New("pendingedits: not in staging mode")
