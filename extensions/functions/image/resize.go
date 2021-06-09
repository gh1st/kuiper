package main

import (
	"bytes"
	"fmt"
	"github.com/emqx/kuiper/xstream/api"
	"github.com/nfnt/resize"
	"image"
	"image/jpeg"
	"image/png"
)

type imageResize struct {
}

func (f *imageResize) Validate(args []interface{}) error {
	if len(args) != 3 {
		return fmt.Errorf("The resize function supports 3 parameters, but got %d", len(args))
	}
	return nil
}

func (f *imageResize) Exec(args []interface{}, _ api.FunctionContext) (interface{}, bool) {
	arg, ok := args[0].([]byte)
	if !ok {
		return fmt.Errorf("arg[0] is not a bytea, got %v", args[0]), false
	}
	width, ok := args[1].(int)
	if !ok || 0 > width {
		return fmt.Errorf("arg[1] is not a bigint, got %v", args[1]), false
	}
	height, ok := args[2].(int)
	if !ok || 0 > height {
		return fmt.Errorf("arg[2] is not a bigint, got %v", args[2]), false
	}
	img, format, err := image.Decode(bytes.NewReader(arg))
	if nil != err {
		return fmt.Errorf("image decode error:%v", err), false
	}
	img = resize.Resize(uint(width), uint(height), img, resize.Bilinear)

	var b []byte
	buf := bytes.NewBuffer(b)
	switch format {
	case "png":
		err = png.Encode(buf, img)
	case "jpeg":
		err = jpeg.Encode(buf, img, nil)
	default:
		return fmt.Errorf("%s image type is not currently supported", format), false
	}
	if nil != err {
		return fmt.Errorf("image encode error:%v", err), false
	}
	return buf.Bytes(), true
}

func (f *imageResize) IsAggregate() bool {
	return false
}
