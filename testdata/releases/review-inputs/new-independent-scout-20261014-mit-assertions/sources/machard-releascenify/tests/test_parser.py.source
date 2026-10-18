import json
import os
import pytest
from releascenify.parser import parse_filename

def load_test_cases(category):
    filename = f'test_cases_{category}.json'
    cases_file = os.path.join(os.path.dirname(__file__), filename)
    with open(cases_file, 'r', encoding='utf-8') as f:
        data = json.load(f)
    
    cases = []
    for case in data:
        # Use filename as ID for nice pytest output
        cases.append(pytest.param(case['filename'], case['expected'], id=case['filename']))
    return cases

def run_parse_test(filename, expected):
    result = parse_filename(filename)
    for key, expected_val in expected.items():
        actual_val = result.get(key)
        if isinstance(expected_val, list):
            # Sort lists for comparison (like languages)
            assert sorted(actual_val) == sorted(expected_val), f"Failed on '{filename}': expected {key}={expected_val}, got {actual_val}"
        else:
            assert actual_val == expected_val, f"Failed on '{filename}': expected {key}={expected_val}, got {actual_val}"

@pytest.mark.parametrize("filename,expected", load_test_cases('movies'))
def test_movie_parser(filename, expected):
    run_parse_test(filename, expected)

@pytest.mark.parametrize("filename,expected", load_test_cases('series'))
def test_series_parser(filename, expected):
    run_parse_test(filename, expected)

def test_invalid_filenames():
    with pytest.raises(ValueError, match="Filename contains no valid metadata"):
        parse_filename("Ana201100AZWBLDPH24ADSY.rar")
    
    with pytest.raises(ValueError, match="Filename contains no valid metadata"):
        parse_filename("Abon202100pAZWEDDP024ADSK.rar")
        
    with pytest.raises(ValueError, match="Filename contains no valid metadata"):
        parse_filename("MDMN65M4.rar")

    with pytest.raises(ValueError, match="Filename contains no valid metadata"):
        parse_filename("TheAohcrDireS2100BuaRMXACFLC0NN.part01.rar")

    with pytest.raises(ValueError, match="Filename contains no valid metadata"):
        parse_filename("Le Chant des forêts (Bande originale du film) [FLAC 16Bit].rar")

    with pytest.raises(ValueError, match="Filename contains no valid metadata"):
        parse_filename("V La s\ufffdrie.rar")

    # Verify that a filename with metadata parses correctly
    res = parse_filename("Avatar.2009.mkv")
    assert res['title'] == 'Avatar'
    assert res['year'] == 2009

if __name__ == '__main__':
    pytest.main([__file__, '-v'])
