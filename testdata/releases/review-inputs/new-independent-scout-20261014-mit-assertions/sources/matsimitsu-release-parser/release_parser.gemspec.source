# -*- encoding: utf-8 -*-
require File.expand_path('../lib/release_parser/version', __FILE__)

Gem::Specification.new do |gem|
  gem.authors       = [
    'Robert Beekman'
  ]
  gem.email                 = ['releaseparser@matsimitsu.nl']
  gem.description           = 'A Ruby scene release parser'
  gem.homepage              = 'https://github.com/matsimitsu/release_parser'
  gem.license               = 'MIT'
  gem.summary               = 'A Ruby lib that attempts to parse scene releases'
  gem.files                 = `git ls-files`.split($\)
  gem.executables           = gem.files.grep(%r{^bin/}).map{ |f| File.basename(f) }
  gem.test_files            = gem.files.grep(%r{^(test|spec|features)/})
  gem.name                  = 'release_parser'
  gem.require_paths         = ['lib']
  gem.version               = ReleaseParser::VERSION
  gem.required_ruby_version = '>= 1.9'

  gem.add_development_dependency 'rake'
  gem.add_development_dependency 'rspec'
end
