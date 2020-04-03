This provides a very basic simulator written in golang to assess the probability of slice code collisions in a population of devices. A population of random
receiving devices are created with a simulated random trace received during an epoch, matching the defined profile of transmission. A number of different
simulated disclosures are then compared against these traces to determine if there are any matches (which there should not be, as these devices have
been receiving random simulated codes unrelated to the transmitter). The algorithm uses a sliding window to look for a cluster of matches in a group,
with parameters controlled from the configuration file.

This simplistic simulation is limited by memory size. A revised approach is needed for larger scale simulations.

Simply build with "go build" and invoke with command line parameter of the "config.json" file.
