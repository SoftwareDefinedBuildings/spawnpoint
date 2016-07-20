/*
This is a convenience package for supplying configuration parameters and applying metadata
to a BOSSWAVE driver which provides services and interfaces.

The following keys are expected and used by spawnable:
  - svc_base_uri: the full BOSSWAVE URI prefix for where this driver will be deployed
  - metadata: tiered map of key-value pairs to be applied to
    different levels in the driver URI hierarchy
  - metavalid: string formatted like "2006-01-02T15:04:05 MST" specifying when the metadata was
    considered valid

Metadata

Metadata in spawnable is applied as a set of key-value pairs persisted at some URI. The URIs
specified in the file are relative to `svc_base_uri`.

Example:
	# params.yml
	metadata:
		- s.servicename:
			- key1: value1
			- key2: value2
		- s.servicename/instance:
			- key3: value3
		- s.servicename/instance/i.name:
			- key4: value4
			- key5: value5

If `svc_base_uri` was `scratch.ns/services`, then metadata would be placed at the following URIs:

	scratch.ns/services/s.servicename/!meta/key1
	scratch.ns/services/s.servicename/!meta/key2
	scratch.ns/services/s.servicename/instance/!meta/key3
	scratch.ns/services/s.servicename/instance/i.name/!meta/key4
	scratch.ns/services/s.servicename/instance/i.name/!meta/key5

Inheritance of this metadata is dictated by the semantics of the BOSSWAVE query tool, which will
"trickle down" metadata from prefixes to the longer URIs.
*/
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

// Key value structure representing the paramater file
type Params struct {
	dat map[string]interface{}
}

// Loads parameters from the given YAML file
func GetParamsFile(filename string) (p *Params, e error) {
	defer func() {
		r := recover()
		if r != nil {
			p = nil
			e = fmt.Errorf("Malformed YAML file")
			return
		}
	}()
	params := make(map[string]interface{})
	in, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	//Strip out all /* */ comments
	//we will strip them even inside yaml components
	out := make([]byte, 0, len(in))
	level := 0
	for idx := 0; idx < len(in); idx++ {
		if in[idx] == '/' && in[idx+1] == '*' {
			idx += 1
			level += 1
			continue
		}
		if in[idx] == '*' && in[idx+1] == '/' {
			idx += 1
			level -= 1
			if level < 0 {
				return nil, fmt.Errorf("unmatched */")
			}
			continue
		}
		if level == 0 {
			out = append(out, in[idx])
		}
	}
	err = yaml.Unmarshal(out, &params)
	if err != nil {
		return nil, err
	}
	return &Params{params}, nil
}

// Loads parameters from the given YAML file or exits
func GetParamsFileOrExit(filename string) *Params {
	params, err := GetParamsFile(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not read %s: %v\n", filename, err)
		os.Exit(1)
	}
	return params
}

// Loads params from YAML file 'params.yml'
func GetParams() (p *Params, e error) {
	return GetParamsFile("params.yml")
}

// Loads params from 'params.yml' or exits
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

func (p *Params) MustStringSlice(key string) []string {
	rv, ok := p.dat[key]
	if !ok {
		fmt.Printf("Missing paramater '%s'\n", key)
	}
	rvi, ok := rv.([]string)
	if !ok {
		var r []string
		if ifl, ok := rv.([]interface{}); ok {
			for _, s := range ifl {
				if str, ok := s.(string); ok {
					r = append(r, str)
				} else {
					fmt.Printf("Parameter '%s' cannot convert all entities to string slice\n", key)
				}
			}
			return r
		} else {
			fmt.Printf("Parameter '%s' should be a string slice\n", key)
		}
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
	for i := 0; i < len(headers); i += 2 {
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
