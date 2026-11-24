// Package vault holds the secrets the owner's credential broker stages for
// the phone, in memory only, and hands each to the driver once. A secret
// reference is the only form in which a secret appears anywhere Pinky
// records or shows text.
package vault

import (
	"errors"
	"strings"
)

const (
	refOpen      = "{{secret:"
	refClose     = "}}"
	maxName      = 40
	errMixed     = "a secret reference must be the whole text; type the rest in another phone_type"
	errMixedCode = "{{code}} must be the whole text; type the rest in another phone_type"
	errBadRef    = "a secret reference is {{secret:NAME}}, NAME of 1-40 characters from a-z, 0-9, dot, dash and underscore"
)

// ParseRef reads the text of a phone_type. isRef is true when the whole text
// is one reference, {{secret:NAME}}. The error is set when the text holds
// "{{secret:" but is not exactly one reference, so a secret never has
// neighbours that could be read back from a field.
func ParseRef(text string) (name string, isRef bool, err error) {
	if !strings.Contains(text, refOpen) {
		return "", false, nil
	}
	if !strings.HasPrefix(text, refOpen) || !strings.HasSuffix(text, refClose) || strings.Count(text, refOpen) != 1 {
		return "", false, errors.New(errMixed)
	}
	name = text[len(refOpen) : len(text)-len(refClose)]
	if !ValidName(name) {
		return "", false, errors.New(errBadRef)
	}
	return name, true, nil
}

// Ref returns the reference text for a name.
func Ref(name string) string { return refOpen + name + refClose }

// ValidName reports whether name can be a secret's name.
func ValidName(name string) bool {
	if name == "" || len(name) > maxName {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

type RefKind int

const (
	RefSecret RefKind = iota + 1
	RefCode
)

const codeRef = "{{code}}"

type ValueRef struct {
	Kind RefKind
	Name string
}

func CodeRef() string { return codeRef }

func ParseValueRef(text string) (ValueRef, bool, error) {
	if text == codeRef {
		return ValueRef{Kind: RefCode}, true, nil
	}
	if strings.Contains(text, codeRef) {
		return ValueRef{}, false, errors.New(errMixedCode)
	}
	name, ok, err := ParseRef(text)
	if err != nil || !ok {
		return ValueRef{}, false, err
	}
	return ValueRef{Kind: RefSecret, Name: name}, true, nil
}
