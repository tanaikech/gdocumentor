package workspace

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

// ExtractTextFromDOCXBytes extracts plain text paragraphs from an in-memory .docx (Word OpenXML ZIP) archive.
func ExtractTextFromDOCXBytes(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}

	var docFile *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			docFile = f
			break
		}
	}
	if docFile == nil {
		return "", nil
	}

	rc, err := docFile.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	xmlBytes, err := io.ReadAll(rc)
	if err != nil {
		return "", err
	}

	dec := xml.NewDecoder(bytes.NewReader(xmlBytes))
	var paragraphs []string
	var currentPara strings.Builder
	inText := false

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch se := tok.(type) {
		case xml.StartElement:
			switch se.Name.Local {
			case "t":
				inText = true
			case "tab":
				currentPara.WriteString("\t")
			case "br", "cr":
				currentPara.WriteString("\n")
			}
		case xml.EndElement:
			switch se.Name.Local {
			case "t":
				inText = false
			case "p":
				pStr := strings.TrimSpace(currentPara.String())
				if pStr != "" {
					paragraphs = append(paragraphs, pStr)
				}
				currentPara.Reset()
			}
		case xml.CharData:
			if inText {
				currentPara.Write(se)
			}
		}
	}

	if rem := strings.TrimSpace(currentPara.String()); rem != "" {
		paragraphs = append(paragraphs, rem)
	}
	return strings.Join(paragraphs, "\n"), nil
}
