package spawnable

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"

	yaml "gopkg.in/yaml.v2"
)

type Params struct {
	dat map[string]interface{}
}

func GetParams() (*Params, error) {
	params := make(map[string]interface{})
	paramContents, err := ioutil.ReadFile("params.yml")
	if err != nil {
		return nil, err
	}
	err = yaml.Unmarshal(paramContents, &params)
	if err != nil {
		return nil, err
	}
	return &Params{params}, nil
}
func GetParamsOrExit() *Params {
	params, err := GetParams()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not read params.yml: %v\n", err)
		os.Exit(1)
	}
	return params
}

func (p *Params) GetEntity() (blob []byte, err error) {
	v, ok := p.dat["entityx"]
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
func (p *Params) GetEntityOrExit() (blob []byte) {
	blob, err := p.GetEntity()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not get entity from params: %v\n", err)
		os.Exit(1)
	}
	return blob
}
func (p *Params) MustString(key string) string {
	rv, ok := p.dat[key]
	if !ok {
		fmt.Printf("Missing paramater '%s'\n", key)
	}
	rvs, ok := rv.(string)
	if !ok {
		fmt.Printf("Parameter '%s' should be a string\n", key)
	}
	return rvs
}
func (p *Params) MustInt(key string) int {
	rv, ok := p.dat[key]
	if !ok {
		fmt.Printf("Missing paramater '%s'\n", key)
	}
	rvi, ok := rv.(int)
	if !ok {
		fmt.Printf("Parameter '%s' should be an int\n", key)
	}
	return rvi
}

func DoHttpPutStr(uri, content string, headers []string) string {
	client := &http.Client{}
	req, err := http.NewRequest("PUT",
		uri, bytes.NewBufferString(content))
	if err != nil {
		fmt.Println("Got HTTP PUT error: ", err)
		return ""
	}
	if len(headers)%2 != 0 {
		fmt.Println("Odd number of headers")
		return ""
	}
	for i := 0; i < len(headers)/2; i += 2 {
		req.Header.Add(headers[i], headers[i+1])
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Got HTTP PUT error: ", err)
		return ""
	}
	contents, _ := ioutil.ReadAll(resp.Body)
	return string(contents)
}
