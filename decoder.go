package gbasm2

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
)

type XInstructionSet struct {
	Name          string          `xml:"name,attr"`
	XInstructions []*XInstruction `xml:"Instruction"`
}

type XInstruction struct {
	Name    string   `xml:"name,attr"`
	Summary string   `xml:"summary,attr"`
	Forms   []*XForm `xml:"InstructionForm"`
}

type XForm struct {
	Attrs     []xml.Attr    `xml:",any,attr"`
	Operands  []*XOperand   `xml:",any"`
	Encodings []*XEncodings `xml:"Encoding"`
}

type XOperand struct {
	XMLName xml.Name
	ID      string `xml:"id,attr"`
	Type    string `xml:"type,attr"`
	Input   bool   `xml:"input,attr"`
	Output  bool   `xml:"output,attr"`
}

type XEncodings struct {
	Encodings []*XEncoding `xml:",any"`
}

type XEncoding struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
}

func (e *XEncoding) GetAttr(k string) (v string, ok bool) {
	for _, a := range e.Attrs {
		if a.Name.Local == k {
			return a.Value, true
		}
	}
	return "", false
}

func Dump(is *XInstructionSet) {
	fmt.Printf("Instruction Set: %s\n", is.Name)
	for _, i := range is.XInstructions {
		fmt.Printf("\tInstruction %s (%s)\n", i.Name, i.Summary)
		for _, f := range i.Forms {
			fmt.Printf("\t\t")
			for _, a := range f.Attrs {
				fmt.Printf("%s:%s ", a.Name, a.Value)
			}
			fmt.Printf("\n\t\t\t")
			for _, op := range f.Operands {
				fmt.Printf("%v %s (input %t, output %t) ", op.XMLName, op.Type, op.Input, op.Output)
			}
			fmt.Printf("\n\t\t\t\t")
			for _, eb := range f.Encodings {
				fmt.Printf("\n\t\t\t\t")
				for _, b := range eb.Encodings {
					//fmt.Printf("%#v", b)
					fmt.Printf("(%s ", b.XMLName.Local)
					for _, a := range b.Attrs {
						fmt.Printf("%s=%s ", a.Name.Local, a.Value)
					}
					fmt.Printf(") ")
				}
			}
			fmt.Printf("\n")
			//fmt.Printf("Encoding (%d bytes)\n", len(f.Encoding))
		}
	}
}

func DecodeFile(fname string) (*XInstructionSet, error) {
	f, err := os.Open(fname)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	instrs := &XInstructionSet{}
	d := xml.NewDecoder(f)
	err = d.Decode(instrs)
	if err != nil {
		return nil, err
	}
	return instrs, nil
}

func decode(r io.Reader) (*XInstructionSet, error) {
	instrs := &XInstructionSet{}
	d := xml.NewDecoder(r)
	err := d.Decode(instrs)
	if err != nil {
		return nil, err
	}
	return instrs, nil
}
