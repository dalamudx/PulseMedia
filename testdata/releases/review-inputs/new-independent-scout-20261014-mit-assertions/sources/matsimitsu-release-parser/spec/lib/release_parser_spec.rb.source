require 'spec_helper'

describe 'ReleaseParser' do
  let(:input)  { "Looped (2016) S01E14A Space Burrito 720p HDTV x264-DEADPOOL" }
  let(:parser) { ::ReleaseParser::Parser.new(input) }
  let(:result) { parser.parse }

  it "should parse a tv release" do
    expect( result.title         ).to eql('Looped')
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
