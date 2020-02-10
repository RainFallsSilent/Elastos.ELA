// Copyright (c) 2017-2020 The Elastos Foundation
// Use of this source code is governed by an MIT
// license that can be found in the LICENSE file.
//

package main

import (
	"fmt"
	"io/ioutil"
	"os"

	"github.com/elastos/Elastos.ELA/picturetobase58/base58"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Println("please input file path")
		return
	}
	path := os.Args[1]
	fmt.Println("processing",path, "please wait ...")
	file, err := ioutil.ReadFile(path)
	if err != nil {
		fmt.Println("err1", err)
		return
	}
	data := base58.Encode(file)

	fmt.Println("base58 length:", len(data))

	rFile, err := os.OpenFile("result", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		fmt.Println(err)
		return
	}

	_, err = rFile.Write([]byte(data))
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println("succeed, see the result in result file.")
}
