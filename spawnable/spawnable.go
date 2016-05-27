package spawnable

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"time"

	"gopkg.in/immesys/bw2bind.v5"

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

const tsFMT = "2006-01-02T15:04:05 MST"

type MetaTuple struct {
	Val string `yaml:"val"`
	TS  string `yaml:"ts"`
	key string
}

func (mt *MetaTuple) NewerThan(t time.Time) bool {
	mttime, err := time.Parse(tsFMT, mt.TS)
	if err != nil {
		fmt.Println("BAD METADATA TIME TAG: ", err)
		return false
	}
	return mttime.After(t)
}

func (p *Params) MergeMetadata(cl *bw2bind.BW2Client) {
	metavalidI, haveValid := p.dat["metavalid"]
	vtime := time.Now()
	if haveValid {
		metavalidS := metavalidI.(string)
		var err error
		vtime, err = time.Parse(time.UnixDate, metavalidS)
		if err != nil {
			fmt.Println("Error parsing valid time:", err)
			os.Exit(1)
		}
	}
	ourtuples := []*MetaTuple{}
	for ki, vi := range p.dat["metadata"].(map[interface{}]interface{}) {
		k := ki.(string)
		v := vi.(string)
		mt := &MetaTuple{Val: v, TS: vtime.Format(tsFMT), key: k}
		ourtuples = append(ourtuples, mt)
	}

	doTuple := func(tgt string, mt *MetaTuple) {
		mttime, err := time.Parse(tsFMT, mt.TS)
		if err != nil {
			fmt.Println("Metadata tag has bad timestamp:", tgt)
			return
		}
		ex_metadata, err := cl.QueryOne(&bw2bind.QueryParams{
			URI:       tgt,
			AutoChain: true,
		})
		if err != nil {
			fmt.Println("Could not query metadata: ", err)
			return
		}
		if ex_metadata != nil {
			entry, ok := ex_metadata.GetOnePODF(bw2bind.PODFSMetadata).(bw2bind.MsgPackPayloadObject)
			if ok {
				obj := bw2bind.MetadataTuple{}
				entry.ValueInto(&obj)
				if !mt.NewerThan(obj.Time()) {
					fmt.Println("Existing metadata is same/newer for: ", tgt)
					return
				}
			}
		}
		po, err := bw2bind.CreateMsgPackPayloadObject(bw2bind.PONumSMetadata, &bw2bind.MetadataTuple{
			Value:     mt.Val,
			Timestamp: mttime.UnixNano(),
		})
		if err != nil {
			fmt.Println("Could not create PO: ", err)
		}

		err = cl.Publish(&bw2bind.PublishParams{
			URI:            tgt,
			PayloadObjects: []bw2bind.PayloadObject{po},
			Persist:        true,
			AutoChain:      true,
		})
		if err != nil {
			fmt.Println("Unable to update metadata: ", err)
		} else {
			fmt.Printf("set %s to %v @(%s)\n", tgt, mt.Val, mt.TS)
		}
	}

	for _, mt := range ourtuples {
		tgt := p.MustString("svc_base_uri") + "!meta/" + mt.key
		doTuple(tgt, mt)
	}
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
