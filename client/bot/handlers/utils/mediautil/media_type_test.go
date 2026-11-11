package mediautil

import (
	"testing"

	"github.com/krau/SaveAny-Bot/database"
)

func TestUserMediaFilterDefensive(t *testing.T) {
	if f := UserMediaFilter(nil); f != nil {
		t.Fatal("nil user = no filter")
	}
	if f := UserMediaFilter(&database.User{}); f != nil {
		t.Fatal("empty column = no filter")
	}
	if f := UserMediaFilter(&database.User{MediaFilter: "bogus,type"}); f != nil {
		t.Fatal("corrupt column must degrade to no filter")
	}
	if f := UserMediaFilter(&database.User{MediaFilter: "video"}); f == nil || !f.Allows("video") {
		t.Fatal("valid column must resolve")
	}
}
