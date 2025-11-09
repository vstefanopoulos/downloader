package downloader

import "errors"

var (
	ErrUnexpectedStatus = errors.New("unexpected status")
	ErrRangeDownload    = errors.New("range download error")
	ErrGetDownload      = errors.New("get download error")
	ErrGetHeader        = errors.New("get header error")
	ErrRunDownload      = errors.New("run download error")
	ErrPreFlightTests   = errors.New("pre flight tests error")
	ErrGetLastModified  = errors.New("get last modified error")
	ErrCloseAndRename   = errors.New("close and rename error")
	ErrOpenFile         = errors.New("open file error")
	ErrCopy             = errors.New("copy error")
	ErrLimitedRead      = errors.New("rate limited read error")
)
