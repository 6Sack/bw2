package bw2

import (
	"fmt"
	"testing"
)

func TestBasic0(t *testing.T) {
	bw := OpenBWContext(nil)
	// f := func(s string) {
	// 	fmt.Printf("Got: %v", s)
	// }
	client := bw.CreateClient(func() {
		fmt.Println("Queue changed")

	})
	client.Subscribe("/a/+/+", false)
	client.Publish("/a/b", "foo")
	//client.Publish("/a/b/c", "foo")
}
