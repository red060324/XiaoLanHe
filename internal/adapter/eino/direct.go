package einoadapter

import (
	"errors"
	"io"

	"github.com/cloudwego/eino/schema"
)

const emptyReply = "您好，请再说具体一点，我好帮您。"

type directStream struct {
	stream *schema.StreamReader[*schema.Message]
	model  string
	suffix string
	emit   bool
	wrote  bool
}

func (s *directStream) Recv() (string, error) {
	for {
		message, err := s.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) && !s.emit && (!s.wrote || s.suffix != "") {
				s.emit = true
				if !s.wrote {
					return emptyReply + s.suffix, nil
				}
				return s.suffix, nil
			}
			return "", err
		}
		if message.Content != "" {
			s.wrote = true
			return message.Content, nil
		}
	}
}

func (s *directStream) Close()        { s.stream.Close() }
func (s *directStream) Model() string { return s.model }
