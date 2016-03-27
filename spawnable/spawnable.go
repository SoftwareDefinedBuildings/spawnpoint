package spawnable

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"os"

	yaml "gopkg.in/yaml.v2"
)

func GetParams() (map[string]interface{}, error) {
	params := make(map[string]interface{})
	paramContents, err := ioutil.ReadFile("params.yml")
	if err != nil {
		return nil, err
	}
	err = yaml.Unmarshal(paramContents, &params)
	if err != nil {
		return nil, err
	}
	return params, nil
}
func GetParamsOrExit() map[string]interface{} {
	params, err := GetParams()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not read params.yml: %v\n", err)
		os.Exit(1)
	}
	return params
}
func GetEntity(params map[string]interface{}) (blob []byte, err error) {
	v, ok := params["entityx"]
	if !ok {
		return nil, errors.New("Could not decode entity\n")
	}
	vs, ok := v.(string)
	if !ok {
		return nil, errors.New("Could not decode entity\n")
	}
	eblob, err := base64.StdEncoding.DecodeString(vs)
	if err != nil {
		return nil, err
	}
	return eblob, nil
}
func GetEntityOrExit(params map[string]interface{}) (blob []byte) {
	blob, err := GetEntity(params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not get entity from params: %v\n", err)
		os.Exit(1)
	}
	return blob
}
