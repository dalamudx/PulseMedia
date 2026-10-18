# ReleaseParser

An experiment to see if we can write a scene release tokeniser for fun.

[![Build Status](
http://img.shields.io/travis/matsimitsu/release_parser.svg?style=flat
)](https://travis-ci.org/matsimitsu/release_parser)


## Installation

In your Gemfile:
```
gem 'release_parser'
```

## Usage
Example:

```
describe "ReleaseParser" do
  let(:input)  { "Looped (2016) S01E14A Space Burrito 720p HDTV x264-DEADPOOL" }
  let(:parser) { ReleaseParser::Parser.new(input) }
  let(:result) { parser.parse }

  it "should parse a tv release" do
    expect( result.title         ).to eql('Looped (2016)')
    expect( result.season        ).to be 1
    expect( result.episode       ).to be 14
    expect( result.resolution    ).to eq '720p'
    expect( result.source        ).to eq 'hdtv'
    expect( result.codec         ).to eq 'x264'
    expect( result.release_group ).to eq 'DEADPOOL'
    expect( result.year          ).to eq 2016
    expect( result.audio         ).to be_empty
    expect( result.language      ).to be_empty
    expect( result.freeleech?    ).to be false
  end
end
```

## Contributing

* Fork and create a topic branch.
* Submit a pull request
* Don't expect too much, it's a side project

## Copyright

See LICENSE for details
