# Shmipc RoadMap

## New Features:

### 1.max write buffer of stream
Give restrictions to the write buffer of the stream, so that the writing side will be blocked when the write buffer is full. The writing side will only be unblocked and allowed to continue writing when the receiving process finishes reading the data.

This can prevent a situation where the writing rate is faster than the reading rate of the receiving process, which would quickly fill up the shared memory and trigger the slower fallback path, resulting in degraded program performance.


### 2. Abstract process synchronization mechanisms
Currently, the main methods used for process synchronization are Unix domain sockets or TCP loopback, which are suitable for online ping-pong scenarios. However, by abstracting the process synchronization mechanisms, we can introduce different synchronization mechanisms to adapt to different scenarios and improve program performance.

### 3. Add process synchronization mechanisms with timed synchronization

For offline scenarios (not sensitive to latency), we can use high-interval sleep and polling of flag bits in shared memory for synchronization, which can effectively reduce the overhead of process synchronization and improve program performance.

### 4. Support for ARM architecture


## Optimization:

- Optimize the performance of the fallback path by reducing unnecessary data packet copies.

- Implementation of a lock-free IO queue.

- Consider removing the BufferWriter and BufferReader interfaces and replacing them with concrete implementations to improve performance when serializing and deserializing data.



