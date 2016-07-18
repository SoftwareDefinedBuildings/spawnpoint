package spawnable

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/immesys/bw2bind.v5"
)

const tsFMT = "2006-01-02T15:04:05 MST"

type MetaTuple struct {
	Val string `yaml:"val"`
	TS  string `yaml:"ts"`
	key string
}

// Returns true if the current valid time of the metadata tuple is newer/more recent
// than the given time
func (mt *MetaTuple) NewerThan(t time.Time) bool {
	mttime, err := time.Parse(tsFMT, mt.TS)
	if err != nil {
		fmt.Println("BAD METADATA TIME TAG: ", err)
		return false
	}
	return mttime.After(t)
}

// pulls the k/v pairs from the given URI suffix under the 'metadata' tag in params
func (p *Params) ParamsMetadataFromURI(uriSuffix string) []*MetaTuple {
	var res = []*MetaTuple{}
	// find metadata
	_allMD, found := p.dat["metadata"]
	if !found {
		fmt.Println("Could not find 'metadata' map in params")
		return res
	}
	// check for map
	allMD, ok := _allMD.(map[interface{}]interface{})
	if !ok {
		fmt.Println("'metadata' key in params was not a map")
		return res
	}

	// fetch uri suffix metadata
	_suffixMD, found := allMD[uriSuffix]
	if !found {
		fmt.Printf("No metadata for suffix %s found in params 'metadata'\n", uriSuffix)
		return res
	}
	suffixMD, ok := _suffixMD.(map[interface{}]interface{})
	if !ok {
		fmt.Printf("Metadata for suffix %s was not a map\n", uriSuffix)
		return res
	}

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

	// now its ok
	for k, v := range suffixMD {
		res = append(res, &MetaTuple{Val: v.(string), TS: vtime.Format(tsFMT), key: k.(string)})
	}
	return res
}

// fills in the metadata tuple on the given targetURI if it is newer than the existing metadata
func (p *Params) doTuple(targetURI string, mt *MetaTuple, cl *bw2bind.BW2Client) {
}

// given a k/v map of metadata, applies these as MetadataTuples on the given uriSuffix, which will
// be prefixed with `svc_base_uri`, considered valid from the time provided in `metavalid`
func (p *Params) MergeMetadataOnURI(md []*MetaTuple, uriSuffix string, cl *bw2bind.BW2Client) {
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
	baseURI := strings.TrimSuffix(p.MustString("svc_base_uri"), "/") + "/" + uriSuffix
	baseURI = strings.TrimSuffix(baseURI, "/")
	for _, mt := range md {
		targetURI := baseURI + "/!meta/" + mt.key
		fmt.Printf("Apply %s => %s to %s\n", mt.key, mt.Val, targetURI)
		doTuple(targetURI, mt)
	}
}

// Applies metadata from the different suffixes in the params to their respective URIs
func (p *Params) MergeMetadata(cl *bw2bind.BW2Client) {
	// find metadata
	_allMD, found := p.dat["metadata"]
	if !found {
		fmt.Println("Could not find 'metadata' map in params")
		os.Exit(1)
	}
	// check for map
	allMD, ok := _allMD.(map[interface{}]interface{})
	if !ok {
		fmt.Println("'metadata' key in params was not a map")
		os.Exit(1)
	}
	for _uri, _ := range allMD {
		uri, ok := _uri.(string)
		if !ok {
			fmt.Printf("Top level URI key %v under 'metadata' was not a string\n", _uri)
			os.Exit(1)
		}
		tuples := p.ParamsMetadataFromURI(uri)
		p.MergeMetadataOnURI(tuples, uri, cl)
	}
}
