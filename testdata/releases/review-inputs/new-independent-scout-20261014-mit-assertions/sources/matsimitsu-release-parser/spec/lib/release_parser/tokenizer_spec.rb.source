require 'spec_helper'

describe ReleaseParser::Tokens do
  let(:tokenizer) { ::ReleaseParser::Tokenizer.new(input) }
  let(:result)    { tokenizer.tokenize.map(&:class) }

  context "With a TV show" do
    let(:input) { "Looped S01E14A Space Burrito 720p HDTV x264-DEADPOOL" }

    it "should tokenize show to tokens" do
      expect( result ).to eql([
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Season,
        ReleaseParser::Tokens::Episode,
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Resolution,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Source,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Codec,
        ReleaseParser::Tokens::ReleaseGroup
      ])
    end
  end

  context "With a (daily) TV show" do
    let(:input) { "Jimmy Fallon 2016 09 09 James Spader 720p HDTV x264-CROOKS" }

    it "should tokenize show to tokens" do
      expect( result ).to eql([
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Airdate,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Resolution,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Source,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Codec,
        ReleaseParser::Tokens::ReleaseGroup
      ])
    end
  end

  context "with movie" do
    let(:input) { "Now You See Me 2 2016 1080p BluRay TrueHD 7.1 Atmos x264-BluntSlayer" }

    it "should tokenize movie to tokens" do
      expect( result ).to eql([
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Year,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Resolution,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Source,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Audio,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Audio,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Codec,
        ReleaseParser::Tokens::ReleaseGroup
      ])
    end
  end

  context "With a tv show from a specific country" do
    let(:input) { "Last Man Standing US S06E02 HDTV x264-KILLERS" }

    it "should tokenize show to tokens" do
      expect( result ).to eql([
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Country,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Season,
        ReleaseParser::Tokens::Episode,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Source,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Codec,
        ReleaseParser::Tokens::ReleaseGroup
      ])
    end
  end

  context "With hardcoded subs" do
    let(:input) { "Suicide Squad 2016 720p HC HDRip x264 AAC-ETRG" }

    it "should tokenize show to tokens" do
      expect( result ).to eql([
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Year,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Resolution,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::HardCodedSubs,
        ReleaseParser::Tokens::Source,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Codec,
        ReleaseParser::Tokens::Separator,
        ReleaseParser::Tokens::Unknown,
        ReleaseParser::Tokens::ReleaseGroup
      ])
    end
  end
end
