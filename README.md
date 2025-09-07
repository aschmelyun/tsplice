# tsplice

A straight-forward application to **splice** and edit videos from the **t**erminal.

Run with `go run main.go <input-file>` where input-file is a local path to a single video file.

:construction: This is a WIP and is being actively developed for an initial 1.0 version :construction:

## Requirements

You'll need to have `ffmpeg` installed on your local machine to use tsplice. Additionally, you'll have to set an env variable with your OpenAI API key using `export OPENAI_API_KEY={sk-your-key}`.

## How it works

This app performs a few basic steps, using [Charm](https://github.com/charmbracelet) components to make it look nice:

1. Extract audio from the video using ffmpeg
2. Sends the audio to OpenAI's Whisper API for transcription
3. Parses the transcription into individual lines and adds it to a checklist
4. Takes the selected checklist items and compiles them to a list
5. Puts together the final video with ffmpeg

That's it! Your spliced video will be available in the same directory as your original.