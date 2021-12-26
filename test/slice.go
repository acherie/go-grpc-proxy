package main

import "fmt"

func main() {

	s := []string{"a", "b", "c", "d"}
	printSlice(s)

	s1 := s[:2]
	printSlice(s1)

	s1[0] = "x"
	printSlice(s)
	printSlice(s1)
}

func printSlice(s []string) {

	fmt.Printf("len=%d, cpa=%d, slice=%v \n", len(s), cap(s), s)

}
