package pdfscan

import "errors"

var (
	errUnexpectedEnd      = errors.New("unexpected end of data")
	errUnexpectedToken    = errors.New("unexpected token")
	errUnterminatedString = errors.New("unterminated literal string")
	errUnterminatedHex    = errors.New("unterminated hex string")
	errNotIndirectObject  = errors.New("not an indirect object header")
	errNotXRefStream      = errors.New("object is not an xref stream")
)
