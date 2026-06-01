package functions

import "strings"

func IsImageAttachment(contentType string) bool {
	return strings.HasPrefix(contentType, "image/")
}
