#!/bin/sh
# Regenerates the interruption and ambient-chatter clips with the macOS
# `say` voice (16 kHz mono PCM16). f1..f8.wav (the eight facts) were made
# earlier and are kept as they are so baselines stay comparable.
set -e
cd "$(dirname "$0")"
gen() { say -o "$1.wav" --data-format=LEI16@16000 "$2"; }
gen int1 "Wait, stop. Remember this: the parking spot is B14."
gen int2 "Hold on. Remember this instead: the dentist is on Tuesday at nine."
gen int3 "Sorry, one moment. Remember that the gift is in the attic."
gen narr1 "I was thinking about the weather today. It looks like it might rain later this afternoon, so I should probably take a jacket."
gen narr2 "The train was a bit late this morning, but the coffee shop near the station was still open, which was nice."
gen narr3 "My neighbour has been repainting his fence all week. It is a very bright shade of yellow, quite cheerful really."
gen narr4 "We should think about what to cook this weekend. Maybe a soup, or something with rice and vegetables."
gen narr5 "I read an article about how bees find their way home. Apparently they use the position of the sun."
gen narr6 "The office plants are growing well this season. The tall one near the window has a new leaf."
