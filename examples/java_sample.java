import java.util.ArrayList;
import java.util.List;

public class UserCode {
    public Object profileTarget() {
        List<Integer> data = new ArrayList<>();
        for (int i = 0; i < 10000; i++) {
            data.add(i * i);
        }
        long total = 0;
        for (int num : data) {
            total += num;
        }
        try {
            Thread.sleep(100);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
        return total;
    }
}
